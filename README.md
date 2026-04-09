# Hiro

A distributed AI agent platform. A single binary serves an HTTP API, WebSocket chat, and a React dashboard. Agents are defined as markdown files and run agentic loops that can spawn and manage child agents.

## Quick Start

```bash
curl -fsSL https://raw.githubusercontent.com/nchapman/hiro/main/install.sh | sh
cd hiro
docker compose up -d
```

Open [http://localhost:8080](http://localhost:8080) to complete setup — you'll configure a password and LLM provider through the onboarding flow.

Agent state lives in a Docker volume and survives container restarts. The port is bound to localhost only; use a reverse proxy to expose it remotely.

> **Warning:** `docker compose down -v` destroys all agent state (history, memory, todos).

### Docker Requirements

The container requires extra capabilities for per-agent security isolation:

- `CAP_NET_ADMIN` — veth pairs and nftables rules for network isolation
- Custom seccomp profile — allows `CLONE_NEWUSER` for namespace creation (does not use `--privileged`)
- `net.ipv4.ip_forward=1` — routes traffic between agent network namespaces

These are pre-configured in the compose file.

### Updating

```bash
docker compose pull
docker compose up -d
```

## Configuration

On first launch, Hiro starts in setup mode. The dashboard walks you through choosing an LLM provider, entering an API key, and setting an admin password. Configuration is stored in `config/config.yaml` inside the container — no environment variables needed for normal operation.

Provider settings can be updated later through the dashboard settings page.

### Environment Variables

Optional overrides — not required for normal use:

| Variable | Default | Purpose |
|---|---|---|
| `HIRO_ADDR` | `:8080` | HTTP listen address |
| `HIRO_ROOT` | `.` | Platform root containing `agents/`, `instances/`, `skills/`, `workspace/` |
| `HIRO_SWARM_CODE` | *(random)* | Swarm join code for worker discovery |
| `HIRO_LOG_LEVEL` | `info` | Log level |

## Agents

Agents are defined as markdown files in the `agents/` directory:

```
agents/
  operator/
    agent.md          # Required: YAML frontmatter + system prompt
    skills/
      delegate.md     # Skills available to this agent
```

The `agent.md` frontmatter configures the agent's tools and network access:

```yaml
---
name: my-agent
description: Does a thing.
allowed_tools: [Bash, Read, Write, Edit, Glob, Grep]
network:
  egress:
    - "github.com"
    - "*.npmjs.org"
---

Your system prompt goes here.
```

Agent mode (persistent or ephemeral) is a runtime property specified by the caller, not part of the definition. Persistent instances get memory, todos, and history search. Ephemeral instances run a single task and clean up.

Agents can create new agent and skill definitions at runtime using their file tools — no restart needed.

### Skills

Skills use progressive disclosure — only name and description appear in the system prompt. The agent activates a skill on demand, receiving full instructions and any bundled resources.

Skills can be flat `.md` files or directories that bundle scripts, references, and assets alongside a `SKILL.md` file.

## Security Model

Hiro uses defense-in-depth to run untrusted LLM-driven agents:

- **Docker containment** — outer security boundary
- **Process isolation** — each agent runs as a separate OS process; the control plane handles all LLM inference
- **Unix user isolation** — 64 pre-created UIDs with `0700` instance directories
- **Network isolation** — every agent spawns in its own network namespace (default-deny); agents with `network.egress` can reach declared domains via a DNS-driven nftables firewall
- **Per-worker seccomp-BPF** — blocks namespace creation, mount, and ptrace in agent processes
- **Tool capability system** — closed-by-default whitelist with parameterized rules (`Bash(curl *)`) and parent-child inheritance

See [docs/security.md](docs/security.md) for the full threat model.

## Development

Building from source requires Go 1.26+ and Node.js 24+.

```bash
make build           # Build web UI + Go binary
make test            # Run tests in Docker
make test-local      # Run tests locally (mock workers, no Docker)
make lint            # Run golangci-lint
make check           # Tests + go vet (in Docker)
```

To run a development cluster:

```bash
docker compose -f dev/docker-compose.yml up --build
```

See [CLAUDE.md](CLAUDE.md) for detailed architecture, package descriptions, and all available make targets.

## License

MIT

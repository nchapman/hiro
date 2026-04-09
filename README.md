# Hiro

Run AI agents that have their own shell, filesystem, and network — managed through a web dashboard or API. Define agents as markdown files, give them tools, and let them work autonomously or in coordinated teams.

Self-hosted. Single binary. No external dependencies beyond an LLM API key.

## What Hiro Does

- **Chat with agents** through a web dashboard — they can read files, run commands, fetch URLs, and create other agents
- **Define agents in markdown** — system prompt, tool access, and network permissions in a single file
- **Per-agent sandboxing** — every agent runs as its own Unix user in its own network namespace (default-deny)
- **Persistent agents** accumulate memory, maintain todo lists, and search their own history across sessions
- **Multi-agent coordination** — agents spawn children, delegate tasks, and synthesize results
- **Cluster mode** — add worker nodes to distribute agents across machines

## Quick Start

The setup script downloads the latest release, verifies its SHA256 checksum, and creates a `hiro/` directory with two files: `docker-compose.yml` and `seccomp.json`. You can also download these files manually from the [latest release](https://github.com/nchapman/hiro/releases/latest).

```bash
curl -fsSL https://raw.githubusercontent.com/nchapman/hiro/main/setup.sh | sh
cd hiro
docker compose up -d
```

Open [http://localhost:8120](http://localhost:8120) to complete setup — you'll configure a password and LLM provider through the onboarding flow. Once configured, the operator agent is your primary interface. Ask it to create agents, delegate tasks, or work directly.

Agent state lives in a Docker volume and survives container restarts. The port is bound to localhost only; use a reverse proxy to expose it remotely.

> [!WARNING]
> `docker compose down -v` destroys all agent state (history, memory, todos).

### Updating

Re-run the setup script (it prompts before replacing files you've customized), then pull the latest image:

```bash
curl -fsSL https://raw.githubusercontent.com/nchapman/hiro/main/setup.sh | sh
cd hiro
docker compose pull && docker compose up -d
```

## Configuration

On first launch, Hiro starts in setup mode. The dashboard walks you through choosing an LLM provider, entering an API key, and setting an admin password. Configuration is stored in `config/config.yaml` inside the container — no environment variables needed for normal operation.

Provider settings can be updated later through the dashboard settings page.

### Environment Variables

Optional overrides — not required for normal use:

| Variable | Default | Purpose |
|---|---|---|
| `HIRO_ADDR` | `:8120` | HTTP listen address |
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
description: A helper agent.
allowed_tools: [Bash, Read, Write, Edit, Glob, Grep]
# Agents have NO network access by default. Declare allowed domains explicitly:
network:
  egress:
    - "github.com"
    - "*.npmjs.org"
---

Your system prompt goes here.
```

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

The compose file grants `CAP_NET_ADMIN` and a custom seccomp profile to enable per-agent network namespaces and Unix user isolation. No manual configuration needed.

See [docs/security.md](docs/security.md) for the full threat model.

## Development

Building from source requires Go and Node.js (see `go.mod` and `web/ui/package.json` for versions).

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

[MIT](LICENSE)

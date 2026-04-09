# Hiro

A distributed AI agent platform. A single Go binary serves an HTTP API, WebSocket chat, and a React dashboard. Agents are defined as markdown files and run agentic loops that can spawn and manage child agents.

## Quick Start

Hiro runs exclusively in Docker.

```bash
docker compose up --build -d
```

Open `http://localhost:8080` to complete setup — you'll configure a password and LLM provider through the onboarding flow. Configuration is stored in `config/config.yaml` inside the platform root.

Agent state is stored in a Docker volume — it survives container restarts but `docker compose down -v` will destroy it. The port is bound to localhost only; use a reverse proxy to expose it remotely.

### Docker Requirements

The container requires capabilities beyond Docker's defaults for per-agent security isolation:

```yaml
cap_add:
  - NET_ADMIN               # veth pairs + nftables rules for network isolation
security_opt:
  - seccomp=seccomp.json     # allow CLONE_NEWUSER for namespace creation
sysctls:
  - net.ipv4.ip_forward=1    # route traffic between agent network namespaces
```

These are pre-configured in all `docker-compose*.yml` files. The custom seccomp profile (`seccomp.json`) extends Docker's default — it does not grant `CAP_SYS_ADMIN` or use `--privileged`.

## Configuration

On first launch, Hiro starts in setup mode. The dashboard walks you through choosing an LLM provider, entering an API key, and setting an admin password. This is stored in `config/config.yaml` at the platform root — no environment variables needed for normal operation.

Provider configuration can be updated later through the dashboard settings page.

### Environment Variables

These are optional overrides, not required for normal use:

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

Agent mode (persistent or ephemeral) is a runtime property specified by the caller at instance creation time, not part of the definition. Persistent instances get memory, todos, and history search. Ephemeral instances run a single task and clean up.

Agents can create new agent and skill definitions at runtime using their file tools — no restart needed.

### Skills

Skills use progressive disclosure — only name and description appear in the system prompt. The agent activates a skill on demand, receiving full instructions and any bundled resources.

Skills can be flat `.md` files or directories that bundle scripts, references, and assets alongside a `SKILL.md` file.

## Security Model

Hiro uses defense-in-depth to run untrusted LLM-driven agents:

- **Docker containment** — outer security boundary
- **Process isolation** — each agent runs as a separate OS process; the control plane handles all LLM inference
- **Unix user isolation** — 64 pre-created UIDs with `0700` instance directories
- **Network isolation** — agents with `network.egress` spawn in their own network namespace with a DNS-driven nftables firewall; agents without it share the container's network (unrestricted outbound)
- **Per-worker seccomp-BPF** — blocks namespace creation, mount, and ptrace in agent processes
- **Tool capability system** — closed-by-default whitelist with parameterized rules (`Bash(curl *)`) and parent-child inheritance

See [docs/security.md](docs/security.md) for the full threat model.

### Network Isolation

Agents declare allowed egress domains in their frontmatter. Each agent spawns in its own Linux network namespace with a dedicated veth pair. A DNS forwarder in the control plane resolves allowed domains and dynamically populates per-agent nftables IP sets — filtering is at the IP layer, so HTTPS, SSH, git, and any other protocol work transparently.

Agents without `network.egress` share the container's network namespace (unrestricted outbound — a future default-deny posture is planned). Agents with specific domains are confined to those domains. Agents with `egress: ["*"]` have unrestricted access, constrained by parent inheritance.

See [docs/network-isolation.md](docs/network-isolation.md) for the full design.

## Platform Root

On first boot, Hiro initializes the platform root with a default operator agent:

```
/hiro/
  agents/       # Agent definitions (operator-writable)
  skills/       # Shared skills (operator-writable)
  instances/    # Per-agent durable state (memory, persona, sessions)
  workspace/    # Shared collaborative space for agent work
  db/           # Unified SQLite database
  config/       # Operator config + secrets (root-only)
```

## Development

```bash
make build           # Build web UI + Go binary
make test            # Run tests in Docker
make test-local      # Run tests locally (mock workers, no Docker)
make test-isolation  # Run UID isolation tests in Docker
make test-netiso     # Run network isolation tests in Docker
make lint            # Run golangci-lint
make check           # Tests + go vet (in Docker)
```

See [CLAUDE.md](CLAUDE.md) for detailed architecture, package descriptions, and all available make targets.

## License

MIT

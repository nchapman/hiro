# Hiro

Run AI agents that have their own shell, filesystem, and tools — managed through a web dashboard or API. Define agents as markdown files, give them tools, and let them work autonomously or in coordinated teams.

Self-hosted. Single binary. No external dependencies beyond an LLM API key.

## What Hiro Does

- **Chat with agents** through a web dashboard — they can read files, run commands, fetch URLs, and create other agents
- **Define agents in markdown** — system prompt and tool access in a single file
- **Per-agent sandboxing** — Landlock LSM for filesystem isolation + seccomp-BPF for syscall filtering, all unprivileged
- **Persistent agents** accumulate memory, maintain todo lists, and search their own history across sessions
- **Multi-agent coordination** — agents spawn children, delegate tasks, and synthesize results
- **Cluster mode** — add worker nodes to distribute agents across machines

## Quick Start

```bash
mkdir hiro && cd hiro
curl -fsSL https://raw.githubusercontent.com/nchapman/hiro/main/docker-compose.yml -o docker-compose.yml
docker compose up -d
```

Open [http://localhost:8120](http://localhost:8120) to complete setup — you'll configure a password and LLM provider through the onboarding flow. Once configured, the operator agent is your primary interface. Ask it to create agents, delegate tasks, or work directly.

Agent state lives in a Docker volume and survives container restarts. The port is bound to localhost only; use a reverse proxy to expose it remotely.

> [!WARNING]
> `docker compose down -v` destroys all agent state (history, memory, todos).

### Updating

```bash
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
| `HIRO_ROOT` | `/home/hiro` | Platform root; also the container user's `$HOME` |
| `HIRO_SWARM_CODE` | *(random)* | Swarm join code for worker discovery |
| `HIRO_LOG_LEVEL` | `info` | Log level |

### Exposing Host Directories

Agents work inside the container's `workspace/` by default. To give them access to files on the host, bind-mount directories under `/home/hiro/mounts/<name>` in `docker-compose.yml`:

```yaml
services:
  hiro:
    volumes:
      - hiro:/home/hiro
      - /Users/you/Photos:/home/hiro/mounts/photos:ro
      - /Users/you/scratch:/home/hiro/mounts/scratch
```

Hiro auto-discovers each subdirectory under `mounts/` and tells agents about it (including whether it's read-only). No other configuration needed — the `:ro` flag is enforced by the kernel at the mount layer (`MS_RDONLY` returns `EROFS` on writes), same for any read-only network mount.

## Agents

Agents are defined as markdown files in the `agents/` directory:

```
agents/
  operator/
    agent.md          # Required: YAML frontmatter + system prompt
    skills/
      delegate.md     # Skills available to this agent
```

The `agent.md` frontmatter configures the agent's tools:

```yaml
---
name: my-agent
description: A helper agent.
allowed_tools: [Bash, Read, Write, Edit, Glob, Grep, WebFetch]
---

Your system prompt goes here.
```

Agents with `Bash` get network access (for `curl`, `git`, etc.). Agents without `Bash` cannot create IP sockets — enforced by the kernel via seccomp-BPF.

### Skills

Skills use progressive disclosure — only name and description appear in the system prompt. The agent activates a skill on demand, receiving full instructions and any bundled resources.

Skills can be flat `.md` files or directories that bundle scripts, references, and assets alongside a `SKILL.md` file.

## Security Model

Hiro uses defense-in-depth to run untrusted LLM-driven agents:

- **Docker containment** — outer security boundary, runs as non-root `hiro` user (no capabilities)
- **Process isolation** — each agent runs as a separate OS process; the control plane handles all LLM inference
- **Landlock LSM** — kernel-enforced filesystem path whitelist per worker (instance dir, session dir, workspace)
- **seccomp-BPF** — blocks dangerous syscalls (namespace creation, ptrace, io_uring, mount); agents without Bash are restricted to AF_UNIX sockets only (allowlist fails closed on future address families)
- **WebFetch in control plane** — HTTP requests run in the control plane with SSRF protection, not in worker sandboxes
- **Tool capability system** — closed-by-default whitelist with parameterized rules (`Bash(curl *)`) and parent-child inheritance

No capabilities, no custom seccomp profiles, no sidecar files. Just `docker compose up`.

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

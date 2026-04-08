# Security Model

Hiro runs untrusted LLM-driven agents that can execute arbitrary code. The security model uses defense in depth: Docker containment at the outer boundary, OS-level process and user isolation between agents, and a capability system that restricts what tools each agent can use.

## Architecture Overview

```
┌─────────────────────────────────────────────────────┐
│ Docker Container (outer boundary)                   │
│                                                     │
│  Control Plane (root)                               │
│  ├── config.yaml (0600, secrets + tool policies)    │
│  ├── Inference loops (fantasy agent per instance)    │
│  └── Instance lifecycle management                  │
│                                                     │
│  ┌──────────────┐  ┌──────────────┐                 │
│  │ Agent (UID A) │  │ Agent (UID B) │  ...           │
│  │ instances/a/  │  │ instances/b/  │               │
│  │ (0700, own)   │  │ (0700, own)   │               │
│  └──────────────┘  └──────────────┘                 │
│                                                     │
│  /hiro (2775, setgid hiro-agents)                   │
│  ├── agents/, skills/ (hiro-operators)           │
│  └── workspace/ (shared collaborative space)        │
└─────────────────────────────────────────────────────┘
```

## Security Layers

### 1. Docker Containment

The Docker container is the outermost security boundary. The host filesystem, network, and processes are not accessible to agents unless explicitly mounted or exposed.

The container runs Ubuntu 24.04 with common dev tools (git, curl, build-essential, ripgrep, etc.) pre-installed. The control plane runs as root inside the container to manage UID switching. The platform root starts empty — operators mount or copy in only what agents need.

### 2. Process Isolation

Each agent runs as a separate OS process, spawned from the same `hiro` binary with the `agent` subcommand. The control plane and agents communicate over gRPC via Unix sockets — there is no shared memory or in-process state.

**Spawn protocol:**

1. Control plane calls `os/exec.Command("hiro", "agent")` with a dedicated Unix socket path.
2. `SpawnConfig` (instance ID, agent name, tool whitelist, socket paths, etc.) is written as JSON to the child's stdin.
3. The agent process starts a gRPC server on its Unix socket and writes `ready` to stdout.
4. The control plane connects to the agent's socket as a gRPC client.

Worker processes are thin tool-execution sandboxes — they receive `ExecuteTool` RPCs and run tools under the isolated UID. All inference (LLM calls, conversation history, system prompt assembly) happens in the control plane.

### 3. Unix User Isolation

When running in Docker, each agent process runs as a dedicated Unix user from a pre-created pool. This provides OS-enforced file access control between agents.

**Setup:**

- A `hiro-agents` group (GID 10000) and 64 users (`hiro-agent-0` through `hiro-agent-63`, UIDs 10000–10063) are created in the Dockerfile.
- A `hiro-operators` group (GID 10001) is created for operator-mode agents.
- At startup, the control plane checks for the `hiro-agents` group. If present, UID isolation is enabled; if absent (e.g., local development), it is silently disabled.

**Per-agent isolation:**

- When an agent starts, the control plane acquires a UID from the pool and sets `SysProcAttr.Credential` on the child process so it runs as that user.
- The agent's instance directory is `chown`ed to its UID:GID before the process starts.
- Instance directories use `0700` permissions — only the owning agent can read or write its own memory, history, and todos.
- Operator-mode agents receive `hiro-operators` as a supplementary group via `Credential.Groups`, granting write access to `agents/` and `skills/`.
- When an agent stops, its UID is released back to the pool.

**Environment scrubbing:** Under UID isolation, the agent process receives a minimal environment (`PATH`, `HOME={instance-dir}`, `LANG`, `LC_ALL`, `MISE_DATA_DIR`, `MISE_CONFIG_DIR`, `MISE_CACHE_DIR`, `MISE_GLOBAL_CONFIG_FILE`) rather than inheriting the control plane's full environment. Workers explicitly do not receive `HIRO_API_KEY` — inference runs in the control plane, not in workers. Setting `HOME` to the instance directory gives each agent an isolated home for dotfiles, caches, and temp data.

### 4. File System Permissions

| Path | Mode | Owner | Access |
|---|---|---|---|
| `config.yaml` | `0600` | root | Control plane only. Contains secrets and tool policies. Unreadable by agent users. |
| `/hiro` | `2775` (setgid) | root:hiro-agents | Platform root. All agents can read and write. New files inherit the `hiro-agents` group. |
| `agents/` | `2775` (setgid) | root:hiro-operators | Agent definitions. Readable by all (via "other" bits), writable by operator agents only. |
| `skills/` | `2775` (setgid) | root:hiro-operators | Shared skills. Same access as `agents/`. |
| `workspace/` | `0775` | root:hiro-agents | Shared collaborative space. All agents can read and write. |
| `instances/{id}/` | `0700` | agent-user | Private per-agent data (memory, identity, sessions with todos, scratch, tmp). Only the owning agent can access. |
| `db/hiro.db` | default | root | Unified platform database (instances, sessions, messages, usage). Accessed only by the control plane process. |
| `/opt/mise/` | `2775` (setgid) | root:hiro-agents | Shared tool installations (mise, node, python, etc.). All agents can read, write, and install new tools. |
| Agent socket | default | agent-user | gRPC server for control plane→worker calls. Located at `/tmp/hiro-agent-{session-id}.sock`. |

### 5. Tool Capability System

Agent capabilities are controlled by a closed-by-default tool whitelist. An agent can only use a tool if both layers permit it:

```
Effective tools = instance declared tools ∩ parent's effective tools
```

**Instance tool declarations:** Each instance owns its tool declarations in `config.yaml` (root-owned, `0600`). Tools are seeded from the agent definition (`agent.md`) at creation time and can be modified by the operator via the control plane. If no tools are declared, the agent gets no built-in tools.

**Parent inheritance:** A child agent's effective tools are intersected with its parent's effective tools. A child can never have more capabilities than its parent.

**Structural tools** bypass this system — they are intrinsic to the agent's mode:
- `SpawnInstance` is available to all agents.
- Management tools (`ResumeInstance`, `StopInstance`, `DeleteInstance`, `SendMessage`, `ListInstances`) are available to any agent that declares them in `allowed_tools`, scoped to descendants.
- Persistent tools (`TodoWrite`, `AddMemory`, `ForgetMemory`, `HistorySearch`, `HistoryRecall`) are available to persistent agents.

**Parameterized rules:** The `toolrules` package provides fine-grained call-time enforcement beyond tool names. Rules like `Bash(curl *)` restrict which commands an agent can run. Rules are parsed from `allowed_tools` and `disallowed_tools` in agent definitions and operator config. See `docs/tool-permissions.md` for details.

### 6. Secrets Management

Secrets are stored in `config.yaml` (root-only, `0600`) and managed via the `/secrets` slash command in the web UI. They are never sent to agents directly.

**How agents use secrets:**

1. Secret *names* are listed in the agent's system prompt so the LLM knows what's available.
2. Secret *values* are injected as environment variables into bash commands at execution time.
3. The control plane sends secret values with each `ExecuteTool` RPC via the `secret_env` proto field, so changes take effect immediately.

This design ensures secret values never appear in conversation history, system prompts, or LLM context — only in the ephemeral environment of shell commands.

### 7. Agent Authorization Scoping

Agents can only manage their own descendants. This is enforced by the `ScopedManager` wrapper in the inference package.

**How it works:**

1. Each instance's inference loop receives its instance ID as a `callerID` via context propagation.
2. Management tools (`SendMessage`, `StopInstance`, etc.) extract the caller ID from context and create a `ScopedManager` that checks descendant relationships before executing operations.
3. `ScopedManager.checkDescendant()` calls `IsDescendant(targetID, callerID)` via the platform DB. If the target is not a descendant of the caller, the request is rejected.

**Scoping rules:**

| Operation | Authorization |
|---|---|
| `SpawnInstance` | No check needed — caller becomes the parent. |
| `ResumeInstance` | Target must be a descendant of caller. Requires declaration in `allowed_tools`. |
| `SendMessage` | Target must be a descendant of caller. Requires declaration in `allowed_tools`. |
| `StopInstance` | Target must be a descendant of caller. Requires declaration in `allowed_tools`. |
| `DeleteInstance` | Target must be a descendant of caller. Requires declaration in `allowed_tools`. |
| `ListInstances` | Returns only direct children of caller. Requires declaration in `allowed_tools`. |

An agent cannot send messages to, stop, or inspect siblings, ancestors, or unrelated agents.

### 8. IPC Security

All inter-process communication uses gRPC over Unix domain sockets. No TCP ports are opened between workers and the control plane.

**Single socket direction:**

- **Agent sockets** (`/tmp/hiro-agent-{session-id}.sock`): One per worker. The control plane connects as a client to dispatch `ExecuteTool` and `Shutdown` RPCs. Owned by the worker's UID. Under UID isolation, `umask(0002)` makes these group-readable (`0664`).

There is no worker→control plane socket. All inference, instance management, and operator operations happen in-process in the control plane. Workers are pure tool-execution sandboxes with no ability to initiate calls back to the control plane.

gRPC uses `insecure.NewCredentials()` for transport — this is safe because Unix sockets are local-only.

## Threat Model

### What agents CAN do

- Execute arbitrary shell commands (if granted the `Bash` tool).
- Read and write files in the shared workspace (`/hiro/workspace/`, mode `2775`).
- Read agent definitions (`agents/`).
- Spawn ephemeral child agents (with equal or fewer capabilities).
- Make outbound network requests (not restricted by default — see [`docs/network-isolation.md`](network-isolation.md) for the planned per-agent network policy design).

### What agents CANNOT do

- Read other agents' instance data (memory, history, todos) — blocked by `0700` ownership.
- Read `config.yaml` or secret values directly — blocked by `0600` root ownership.
- Manage agents outside their descendant tree — blocked by ScopedManager descendant checks.
- Use tools they weren't granted — blocked by the three-layer capability intersection.
- Write to `agents/` or `skills/` (unless operator mode) — blocked by `hiro-operators` group ownership.
- Rewrite seeded agent definitions — blocked by `0644` root ownership on seeded files.
- Escape the Docker container — standard container isolation applies.

### What the control plane trusts

- The Docker runtime and host kernel.
- The LLM provider API (API keys are sent to it).
- Operator-provided `config.yaml` and agent definitions.

### Limitations

- **Network isolation is domain-level, not IP-level.** Per-agent network namespaces with DNS-driven nftables rules isolate agents. Agents can only reach domains declared in their `network.egress` policy (default: no network). However, if an allowed domain shares an IP with a blocked domain (CDN co-hosting), the agent can reach both. See [`docs/network-isolation.md`](network-isolation.md) for the full design. Requires `CAP_NET_ADMIN` on the container.
- **Shared workspace is collaborative.** Any agent can read or modify files in `/hiro/workspace/`. This is by design for multi-agent collaboration, but means agents must be trusted not to tamper with shared data maliciously.
- **UID pool is finite.** With 64 UIDs, a maximum of 64 concurrent agents can be isolated. Exhaustion returns an error, not a degraded mode.
- **No syscall filtering.** Agents are not confined by seccomp, AppArmor, or similar mechanisms beyond what Docker applies by default.

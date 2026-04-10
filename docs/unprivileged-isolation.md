# Unprivileged Isolation

This document describes a new isolation architecture for Hiro that runs entirely unprivileged. The control plane no longer requires root, CAP_NET_ADMIN, or a custom seccomp profile. The primary deployment target is Ubuntu 24.04 in Docker, but the design works on any Linux 5.19+ host with Landlock enabled.

## Motivation

The current isolation model is effective but complex:

- **64 pre-created Unix users** in the Dockerfile, managed by a UID pool at runtime
- **Control plane runs as root** inside Docker for UID switching and chown
- **Per-agent network namespaces** with veth pairs, nftables IP sets, and a custom DNS forwarder (~1,300 lines)
- **Custom seccomp.json sidecar** that modifies Docker's default profile to allow namespace syscalls
- **Two-phase spawn handshake** (ns-ready → veth setup → self-configure → ready)
- **Group-based filesystem access** (hiro-agents, hiro-operators) with setgid directories and supplementary GID mappings
- **`network.egress` domain lists** that create a false sense of security when enforcement isn't available

Total isolation-specific code: ~3,800 lines. The Dockerfile creates users, groups, and sets ownership. The docker-compose files require `cap_add: [NET_ADMIN]`, `security_opt: [seccomp=seccomp.json]`, and `sysctls: [net.ipv4.ip_forward=1]`.

This complexity makes the system harder to audit, harder to deploy, and harder to run outside Docker. Configuration that can't be enforced everywhere (like domain-level egress rules) is worse than no configuration — it sets the wrong expectations.

The goal is to provide strong isolation with dramatically less code, zero privilege requirements, and no security configuration that might silently go unenforced.

## Design

### Security Model

Two unprivileged Linux mechanisms, self-imposed by each worker process:

| # | Guarantee | Mechanism |
|---|-----------|-----------|
| 1 | Agent cannot access other agents' files, secrets, or database | Landlock LSM (Linux 5.19+) |
| 2 | Agent cannot make dangerous syscalls, and cannot use IP networking unless its tools require it | seccomp-BPF |

No root. No capabilities. No namespaces. No container-level seccomp profile. No sidecar files.

### Core Insight: Tools Determine Network Policy

The current system uses a `network.egress` field in agent definitions to control network access. This requires per-agent network namespaces, veth pairs, nftables, and a DNS forwarder — and it can only be enforced with CAP_NET_ADMIN. Without it, the rules silently do nothing.

The new model eliminates this entirely. Network policy is derived from the agent's **tool declarations**, which are already enforced everywhere:

- **Agent has `Bash`** → worker's seccomp filter allows IP sockets. Bash commands can reach the network.
- **Agent does not have `Bash`** → worker's seccomp filter blocks `socket(AF_INET)` and `socket(AF_INET6)`. No IP networking is possible, period.

**`WebFetch` moves to the control plane** (like memory, todos, and history tools). It's just an HTTP request — no reason it needs to run in the worker. The control plane already has network access for LLM API calls.

Since the agent controls the URL and the control plane fetches it, standard SSRF protections must be applied:
- **Block dangerous destinations:** loopback (127.0.0.0/8), link-local (169.254.0.0/16, including cloud metadata at 169.254.169.254), private networks (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16), and Hiro's own API port.
- **Validate redirect targets:** each redirect hop must be checked against the same blocklist. A common SSRF bypass is a public URL that 302s to `http://127.0.0.1/...`.
- **Response size:** already limited to 64KB per the existing tool implementation.

This means:
- No new configuration. The tool list the operator already writes *is* the network policy.
- No silent non-enforcement. seccomp is a kernel guarantee — it works everywhere, always.
- No `network.egress` field, no domain lists, no ambiguity.
- An agent without Bash can't create a socket even if it tries. Kernel-enforced.

### What Changes

**Landlock replaces the UID pool for filesystem isolation.** Instead of running each agent as a different Unix user with `0700` directories, each agent worker calls `landlock_restrict_self()` at startup with an allowlist of paths it can access. The kernel enforces this — the worker literally cannot open files outside its allowlist, regardless of Unix permissions.

**Per-worker seccomp replaces network namespaces for network isolation.** Workers without Bash get a seccomp filter that blocks IP socket creation. Workers with Bash get a filter that allows it. Both filters block dangerous syscalls (ptrace, io_uring, etc.). No namespaces, no veth, no nftables, no DNS forwarder.

**The control plane runs as a normal user.** No root, no capabilities. The Dockerfile no longer needs `USER root` or capability grants.

**The container-level seccomp.json sidecar is eliminated.** The control plane doesn't call `clone`, `unshare`, `mount`, or any other syscall that Docker's default profile blocks.

### Worker Spawn Path

New spawn sequence (replaces the current two-phase handshake):

```
Control plane                          Worker process
     |                                      |
     |-- fork+exec "hiro agent" ----------->|
     |   (write SpawnConfig to stdin)        |
     |                                      |-- read SpawnConfig
     |                                      |-- prctl(PR_SET_NO_NEW_PRIVS)
     |                                      |-- landlock_restrict_self(path allowlist)
     |                                      |-- seccomp(SET_MODE_FILTER)
     |                                      |     - blocks dangerous syscalls
     |                                      |     - if no Bash: blocks AF_INET/AF_INET6
     |                                      |-- start gRPC server on Unix socket
     |                                      |-- write "ready" to stdout
     |                                      |
     |<-- "ready" --------------------------+
     |-- connect gRPC ---------------------->|
```

Key differences from current:
- **Single-phase handshake.** No ns-ready/veth-ready dance. The worker does everything itself.
- **Worker self-isolates.** The control plane just forks and waits for "ready." No host-side setup.
- **No pipes, no FD passing.** The veth-ready pipe (FD 3) is eliminated.
- **No namespaces.** No CLONE_NEWUSER, CLONE_NEWNET, CLONE_NEWNS, or CLONE_NEWPID.
- **Order matters:** `PR_SET_NO_NEW_PRIVS` first (required by Landlock and seccomp), then Landlock (restricts filesystem), then seccomp (restricts syscalls + optionally blocks network). Each step is strictly more restrictive.
- **Minimal work before isolation.** The worker reads SpawnConfig from stdin then immediately applies all restrictions before opening any files, starting gRPC, or doing anything else. This minimizes the pre-isolation window.

### Landlock Filesystem Policy

Each worker applies a Landlock ruleset at startup:

```
Allowed paths (read + write):
  - instances/<uuid>/          — agent's own instance directory (memory, sessions, todos)
  - sessions/<uuid>/           — session dir (scratch/, tmp/)
  - /tmp/hiro-<session>/       — socket directory for gRPC IPC
  - workspace/                 — shared collaborative space

Allowed paths (read only):
  - agents/                    — agent definitions (read-only for non-operators)
  - skills/                    — shared skills
  - /opt/mise/                 — shared toolchain (node, python, etc.)
  - /usr/, /lib/, /etc/        — system binaries, libraries, config
  - /proc/                     — process info (Go runtime needs /proc/self/exe, net pkg needs /proc/sys/)
  - /dev/                      — /dev/null, /dev/urandom

Denied (by omission — Landlock default-denies unlisted paths):
  - config/                    — secrets, tool policies
  - db/                        — platform database
  - instances/<other-uuid>/    — other agents' data
  - /tmp/ (broadly)            — other agents' socket directories
  - /dev/shm/                  — shared memory (cross-agent communication channel)
```

**Socket directory isolation:** The control plane creates each agent's socket directory (`/tmp/hiro-<session>/`) before spawning the worker. The specific directory path is added to the worker's Landlock ReadWrite list. Workers cannot access other agents' socket directories because only the specific path is allowed, not `/tmp/` broadly.

**`/proc` access:** `/proc` is in the read-only allowlist because the Go runtime requires `/proc/self/exe` (for `os.Executable()`) and the `net` package reads `/proc/sys/net/`. This grants read access to all of `/proc`, including other processes' entries. Sensitive environment variable leakage is mitigated by scrubbing secrets from the control plane's environment at startup (`os.Unsetenv("HIRO_API_KEY")`) and by never placing secrets in worker environments — secrets flow per-tool-call via gRPC, applied only as subprocess env vars for Bash commands.

**`/dev/shm` exclusion:** `/dev/shm` is not in any Landlock allowlist. Landlock's default-deny behavior prevents agents from using POSIX shared memory as a cross-agent communication channel.

**Landlock ABI negotiation:** The implementation auto-detects the kernel's highest supported ABI version and includes all corresponding access rights. ABI v1 (kernel 5.13+) provides the 13 base filesystem access rights. ABI v2 (5.19+) adds `REFER` (cross-directory linking). ABI v3 (6.2+) adds `TRUNCATE`. ABI v5 (6.10+) adds `IOCTL_DEV`. Access rights not declared in the ruleset are implicitly allowed everywhere, so including all known rights is critical. `io_uring` bypass of Landlock is mitigated separately by blocking all three `io_uring` syscalls via seccomp.

**Operator agents** (those with `CreatePersistentInstance` in their effective tools) get read+write access to `agents/` and `skills/` instead of read-only. This replaces the hiro-operators group + setgid mechanism.

**config.yaml protection** no longer relies on root ownership + `0600` permissions. Landlock simply doesn't include `config/` in the allowlist. The worker cannot open it regardless of file permissions.

### seccomp-BPF Filter

Each worker installs a seccomp-BPF filter (unprivileged, self-imposed via `PR_SET_NO_NEW_PRIVS`).

**Always blocked (all workers):**
- `clone(CLONE_NEWUSER)`, `clone(CLONE_NEWNET)` — prevent namespace creation (note: `clone` flags are inspected via BPF argument checking at arg position 0 on x86_64)
- `clone3` — blocked unconditionally (seccomp cannot dereference the `struct clone_args` pointer to inspect flags, so wholesale block is the only safe option)
- `unshare` — prevent namespace creation
- `setns` — prevent entering other namespaces
- `mount`, `umount2` — prevent filesystem manipulation
- `ptrace` — prevent inspecting other processes
- `process_vm_readv`, `process_vm_writev` — prevent cross-process memory access (same-UID processes can use these without ptrace; separate syscalls that must be blocked independently)
- `kexec_load` — prevent loading a new kernel
- `io_uring_setup`, `io_uring_enter`, `io_uring_register` — io_uring can bypass seccomp on some kernels; unnecessary for agent workloads
- `shmget`, `shmat`, `shmctl` — prevent cross-agent shared memory communication

**Conditionally blocked (workers without `Bash` tool):**
- `socket(AF_INET)` — block IPv4 socket creation
- `socket(AF_INET6)` — block IPv6 socket creation
- `socket(AF_UNIX)` remains allowed — needed for gRPC IPC

Workers with the `Bash` tool get IP socket access because Bash commands may need to reach the network (`git clone`, `pip install`, `curl`, etc.).

### Inherited File Descriptors

Landlock restricts future `open()` calls but **does not revoke already-open file descriptors**. When the control plane forks a worker, the child inherits all open FDs. If the control plane has `db/hiro.db` or `config/config.yaml` open, the child could read/write them.

**Mitigation:** Go's `os.OpenFile` sets `O_CLOEXEC` by default, which closes the FD on `exec`. Since workers are spawned via `exec.Command` (fork + exec), `CLOEXEC` handles this. The SQLite library (`modernc.org/sqlite`) also uses `CLOEXEC`. This should be verified during implementation but is expected to work correctly with no code changes.

### Users and Groups

**Current system (deleted):**
- `hiro-agents` group (GID 10000) — primary group for 64 agent users
- `hiro-operators` group (GID 10001) — supplementary group for operator agents
- 64 users `hiro-agent-0` through `hiro-agent-63` (UIDs 10000-10063)
- Control plane runs as root

**New system:**

A single non-root user runs the entire platform — control plane and all workers.

```dockerfile
RUN groupadd -g 10000 hiro && useradd -u 10000 -g hiro -m -s /bin/bash hiro
USER hiro
```

One user, one group. All files are owned by `hiro:hiro`. Filesystem isolation between agents is enforced by Landlock, not Unix permissions. The `hiro-agents` and `hiro-operators` groups are eliminated.

**Operator write access to `agents/` and `skills/`** is enforced by Landlock policy, not filesystem groups. Operator agents get `agents/` and `skills/` in their Landlock write allowlist; other agents get read-only.

**`/opt/mise`** is readable by all (standard permissions). No group ownership tricks needed.

**`config/` directory** is protected by Landlock (not in any agent's allowlist), not by `0600` root ownership.

### docker-compose.yml Changes

Before:
```yaml
services:
  hiro:
    build: .
    cap_add:
      - NET_ADMIN
    security_opt:
      - seccomp=seccomp.json
    sysctls:
      - net.ipv4.ip_forward=1
    ports:
      - "127.0.0.1:8120:8120"
```

After:
```yaml
services:
  hiro:
    build: .
    ports:
      - "127.0.0.1:8120:8120"
```

No capabilities, no custom seccomp, no sysctls.

### Graceful Degradation

The system probes for Landlock at startup:

| Probe | How | If unavailable |
|-------|-----|----------------|
| Landlock | `landlock_create_ruleset(LANDLOCK_CREATE_RULESET_VERSION)` | Log warning. Filesystem isolation disabled. Workers run with full filesystem access. |
| seccomp-BPF | Always available (requires only `PR_SET_NO_NEW_PRIVS`) | Fatal — this should never fail on any modern Linux. |

Startup logs report isolation status:

```
INFO  isolation: landlock=yes seccomp=yes
```

or:

```
WARN  isolation: landlock=no (kernel too old) seccomp=yes
WARN  filesystem isolation disabled — all agents share the process user's file access
```

**Docker Desktop on macOS:** Docker Desktop runs a LinuxKit-based VM. The VM kernel (typically 6.6+) may or may not have `CONFIG_SECURITY_LANDLOCK=y`. If unavailable, graceful degradation applies — acceptable for development, surfaced clearly in logs and UI.

## What Gets Deleted

### Code (~3,500+ lines removed)

| Package/File | Lines | What it did |
|---|---|---|
| `internal/netiso/` | 1,278 | veth pairs, nftables, DNS forwarder, IP filtering |
| `internal/netiso/*_test.go` | 388 | Network isolation tests |
| `internal/uidpool/` | 97 | UID pool acquire/release |
| `internal/agent/spawn_linux.go` | 45 | `CLONE_NEWUSER\|NEWNET\|NEWNS` + UID/GID mappings |
| `cmd/hiro/agent_linux.go` (partial) | ~180 | `selfConfigureNetwork()`, `waitForVethReady()`, `activateGroups()` |
| `internal/agent/spawn.go` (partial) | ~100 | `setupVethPipe()`, `cleanupVethPipe()`, veth handshake in `spawnWorkerProcess()` |
| `internal/agent/manager_lifecycle.go` (partial) | ~30 | `acquireUIDAndChown()` |
| `internal/agent/manager_session.go` (partial) | ~15 | `chownDir()` calls |
| `internal/platform/init.go` (partial) | ~50 | Group lookups, operator ownership, setgid setup |

### Infrastructure

| File | What it did |
|---|---|
| `seccomp.json` (~400 lines) | Modified Docker default seccomp profile |
| `dev/update-seccomp.sh` (~114 lines) | Script to regenerate seccomp.json from upstream |
| Dockerfile user pool creation | `groupadd` + `useradd` loop (both test and runtime stages) |
| docker-compose `cap_add`, `security_opt`, `sysctls` | All compose files simplified |

### Concepts removed from the codebase

- UID pool, UID acquisition/release
- GID mappings, supplementary groups, `setgroups()`
- `hiro-agents` and `hiro-operators` Unix groups
- chown dance (instance dirs, session dirs, socket dirs)
- veth pairs, nftables IP sets, nftables chains
- DNS forwarder, DNS domain matching, IP filtering
- Two-phase spawn handshake (ns-ready, veth-ready)
- Bind-mounted `/etc/resolv.conf` and `/etc/hosts`
- Container-level seccomp profile customization
- Root requirement for the control plane
- `CAP_NET_ADMIN` container capability
- `network.egress` configuration field
- Domain-level network filtering

## What Gets Added

### Code (~570 lines added)

| Component | Lines | What it does |
|---|---|---|
| `internal/landlock/` | 196 | Landlock ABI negotiation (v1-v5), ruleset creation, self-restriction. Thin syscall wrappers + non-Linux stubs. |
| `cmd/hiro/agent_linux.go` | 226 | seccomp-BPF filter with placeholder-and-patch offsets, Landlock application, NO_NEW_PRIVS |
| `internal/inference/tools_webfetch.go` | 144 | WebFetch as control plane local tool with SSRF protection (DNS-before-dial, redirect validation) |

### New fields in SpawnConfig

```go
type SpawnConfig struct {
    // ... existing fields (InstanceID, AgentName, SessionDir, etc.) ...

    // LandlockPaths defines the filesystem sandbox for this worker.
    LandlockPaths LandlockPaths `json:"landlock_paths,omitempty"`

    // NetworkAccess is true when the worker needs IP socket access
    // (derived from tool declarations — true if agent has Bash).
    NetworkAccess bool `json:"network_access,omitempty"`

    // Removed: UID, GID, Groups, NetworkEgress, AgentIP, GatewayIP,
    //          SubnetBits, PeerName
}

type LandlockPaths struct {
    ReadWrite []string `json:"rw,omitempty"` // e.g., instance dir, workspace
    ReadOnly  []string `json:"ro,omitempty"` // e.g., agents/, skills/, /opt/mise
}
```

### Changes to agent frontmatter

The `network` section is removed entirely from agent definitions:

```yaml
# Before:
network:
  egress:
    - "github.com"
    - "*.npmjs.org"

# After: (nothing — network access is determined by tool declarations)
```

## Migration

### Breaking changes

1. **`network.egress` is removed.** Existing agent definitions with this field will have it ignored. Network access is now determined by whether the agent has the `Bash` tool.
2. **`WebFetch` moves to the control plane.** Agents that declared `WebFetch` in `allowed_tools` continue to work — the tool still exists, it just executes in the control plane instead of the worker. No agent definition changes needed.
3. **Instance directories no longer chowned.** Existing instances with agent-UID-owned files will work fine — the control plane user can read/write them. No migration needed.
4. **Control plane no longer runs as root.** Docker volumes created by the old root-running container may have root-owned files. First startup after upgrade should chown the platform root to the `hiro` user. The entrypoint can handle this with a one-time migration.

### Rollout

1. Move WebFetch to the control plane (local tool) with SSRF protections.
2. Implement Landlock wrapper and integrate into worker startup.
3. Update per-worker seccomp filter with conditional AF_INET/AF_INET6 blocking and new blocklist additions (process_vm_readv/writev, io_uring_setup, shmget/shmat/shmctl).
4. Verify `O_CLOEXEC` on all control plane file handles (database, config) empirically — ensure no FD leakage to workers.
5. Remove UID pool, chown dance, group machinery from spawn path.
6. Remove `internal/netiso/`, `internal/uidpool/`, seccomp.json, update-seccomp.sh.
7. Simplify Dockerfile (single user, no pool) and docker-compose files (no caps/seccomp/sysctls).
8. Update `docs/security.md` and other design docs to reflect new model.

## Security Comparison

| Property | Old model | New model |
|---|---|---|
| Filesystem isolation | UID pool + `0700` dirs | Landlock (stronger — path-level, not just ownership) |
| Secrets protection | root-owned `0600` | Landlock (not in allowlist — same guarantee, no root needed) |
| Network: no-network agents | Empty network namespace | seccomp blocks socket creation (stronger — can't even create a socket) |
| Network: network agents | Domain-level filtering via nftables | Full host network (weaker — no domain filtering) |
| Syscall restriction | Per-worker seccomp-BPF | Per-worker seccomp-BPF (same, with additions) |
| Process visibility | UID isolation (different users) | /proc readable but secrets scrubbed from env; no sensitive data exposed (weaker) |
| Cross-agent memory | UID isolation blocks process_vm_readv | seccomp blocks process_vm_readv/writev (same) |
| Cross-agent signals | UID isolation blocks kill() | Same user, kill() possible but PIDs not discoverable (weaker) |
| Container requirements | root, CAP_NET_ADMIN, custom seccomp, sysctls | None |
| Lines of isolation code | ~3,800 | ~250 |

### Accepted trade-offs

1. **No domain-level network filtering.** Agents with Bash get full host network. This is a real regression for high-security deployments. The mitigation is tool rules (`Bash(curl *)` deny rules) and the fact that most agents don't need Bash.

2. **No PID namespace isolation.** All workers run as the same user and can read `/proc` broadly (the Go runtime requires this). Mitigated by: secrets are scrubbed from the control plane's environment at startup (`os.Unsetenv`), secrets are never placed in worker environments (they flow per-tool-call via gRPC), and `process_vm_readv`/`process_vm_writev` are blocked by seccomp.

3. **Cross-agent signals.** A worker with Bash could `kill` another worker's process since they share a UID. Mitigated by: while `/proc` is readable and PIDs are discoverable, killing another worker just causes it to be restarted. Agents without Bash cannot call `kill` at all. This is a residual risk for Bash-capable agents — denial of service, not data exfiltration.

4. **Abstract Unix sockets.** Abstract namespace Unix sockets (null-byte prefix) are not filesystem-bound and are **not restricted by Landlock**. Two colluding agents could use abstract sockets for communication or to pass file descriptors via `SCM_RIGHTS`. This requires both agents to cooperate in the attack and is low risk in practice.

### What's stronger

1. **Filesystem isolation is actually stronger.** Landlock denies at the path level — a process literally cannot `open()` a path not in its allowlist. The old UID model relied on directory permissions, which could be weakened by misconfigured permissions or symlink tricks.

2. **Network denial is stronger.** `socket(AF_INET)` blocked by seccomp means the process cannot create an IP socket at all — not even to localhost, not even to private IPs. The old empty-netns model still allowed socket creation (just no routes to send packets).

3. **No configuration to get wrong.** The old model required correct UID pool setup, correct group membership, correct seccomp.json, correct docker-compose flags. Any misconfiguration degraded silently. The new model has no configuration — tools determine policy, Landlock and seccomp are self-applied.

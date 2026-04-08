# Network Isolation

This document captures requirements, threat analysis, options considered, and the chosen design for per-agent network isolation in Hiro.

## Background

Hiro runs untrusted LLM-driven agents that can execute arbitrary shell commands. All agents currently share the container's network namespace. A compromised or manipulated agent with Bash access can:

- Make outbound HTTP requests to exfiltrate data (`curl`, `wget`, raw sockets, `/dev/tcp`)
- Port-scan the container's network and discover other agents' services
- Connect to other agents' gRPC sockets if it guesses the path
- Perform lateral movement to internal network services reachable from the container
- Use DNS tunneling to exfiltrate data covertly

These are not theoretical risks. Recent security evaluations of OpenClaw-family agent frameworks (Wang et al. 2026, Liu et al. 2026) demonstrate 20-80% attack success rates across exfiltration, lateral movement, and discovery categories — even with model-level safety alignment. The key finding: relying on the LLM to refuse dangerous requests is insufficient. Security must be enforced at the OS/network layer.

### What Hiro Already Does Well

Hiro's security model is significantly stronger than OpenClaw and its variants:

- **Control plane / worker split**: Inference runs in the control plane (root). Workers are dumb tool-execution sandboxes. A compromised agent can't tamper with its own system prompt, tool declarations, or conversation history.
- **UID isolation**: Each worker runs as a dedicated Unix user (UIDs 10000-10063). Instance directories are `0700`. Agents can't read each other's memory, history, or todos.
- **Tool capability system**: Closed-by-default with AST-based Bash command analysis (real shell parser, not regex). Deny-first evaluation, fail-closed on uncertainty.
- **Symlink/path traversal prevention**: `resolveAndConfine()` with both lexical and `EvalSymlinks` checking.
- **Environment scrubbing**: Workers receive minimal env vars. No API keys leak to workers.
- **Docker containment**: Outer boundary. Container escape requires a kernel exploit regardless of inner defenses.

### The Gap

Network isolation is the primary remaining weakness. The `docs/security.md` limitations section acknowledges: "No network isolation between agents" and "No syscall filtering."

## Requirements

1. **Per-agent network policy**: Each agent definition specifies what network access it needs. No declaration means no network access (default-deny).

2. **Domain-name-based rules**: Operators configure allowed destinations by hostname, not IP address. Security features that are hard to use won't get used.

3. **Transparent to tools**: `git clone`, `pip install`, `npm install`, `curl`, `wget`, `apt-get`, `ssh` and all other CLI tools must work unmodified when an agent has network access. No special proxy configuration, no patched binaries.

4. **Protocol-agnostic**: Must work for HTTPS, HTTP, SSH, and any other TCP protocol. The solution cannot depend on inspecting application-layer headers (TLS SNI, HTTP Host) because these are increasingly encrypted and protocol-specific.

5. **Docker-native**: Must work within Docker's standard deployment model. No host kernel modules, no custom runtimes. Ideally no capabilities beyond what's strictly necessary.

6. **Inherited like tool capabilities**: A child agent's network access cannot exceed its parent's. The operator's network policy is the ceiling.

7. **Auditable**: All DNS resolutions and network connections are logged per instance.

8. **Simple agent definition UX**:
   ```yaml
   network:
     egress:
       - "github.com"
       - "*.npmjs.org"
       - "pypi.org"
   ```

## Options Considered

### Option 1: seccomp-BPF Socket Blocking

Block `socket(AF_INET)` and `socket(AF_INET6)` at the syscall level in worker processes. Allow `AF_UNIX` for gRPC. Move `WebFetch` to the control plane as the only network-capable tool.

**Pros:**
- Simple (~50 lines of Go)
- No extra Docker capabilities needed
- Kernel-enforced, inherited across fork/exec, irremovable
- Zero impact on Unix socket IPC

**Cons:**
- All-or-nothing. Agents either have full IP networking or none.
- Kills `git clone`, `pip install`, `npm install`, `curl` in Bash — these are core workflows.
- Would need to proxy every network-using command through a control-plane tool, which is impractical for the long tail of CLI tools that need the network.

**Verdict:** Too blunt as a standalone solution. Blocks legitimate workflows that agents need. However, valuable as a **defense-in-depth layer** on top of network namespace isolation — if an agent escapes its network namespace via a kernel exploit, seccomp would prevent it from creating new IP sockets. Consider adding this as a backstop after the primary isolation is in place.

### Option 2: nftables Per-UID Egress Rules (Static IP Sets)

Add `CAP_NET_ADMIN` to the container. Use nftables with `meta skuid` matching to create per-UID egress rules. Pre-resolve hostnames to IPs and populate nftables sets, refreshing periodically.

**Pros:**
- Per-agent policy without major architecture changes
- nftables supports atomic rule replacement, native sets, UID range matching
- Good performance (conntrack handles established connections)

**Cons:**
- **Static IP resolution is fragile.** Pre-resolving hostnames to IPs and refreshing periodically leads to stale IPs, CDN rotation mismatches, and race windows between DNS changes and set refresh.
- Agents share the network namespace — can still discover each other's services on loopback
- DNS tunneling remains possible unless DNS is also proxied

**Verdict:** The right idea (nftables IP sets) but wrong execution (static/periodic resolution). See Option 7 for the improved version.

### Option 3: Multi-Container with `network: none`

Run workers in a separate container with `network_mode: none`. Proxy all network through the control plane.

**Pros:**
- Strongest isolation (separate network namespace per container)
- `network: none` is Docker-native, no capabilities needed

**Cons:**
- Major architecture change — requires a new spawn protocol (can't fork directly into another container)
- Need either Docker socket access (security risk) or a supervisor process in the worker container
- Shared filesystem coordination via volumes adds complexity
- Over-engineered for per-agent granularity (isolation is per-container, not per-agent)

**Verdict:** Too much architecture disruption for the benefit. The spawn protocol redesign is the risk — it touches the core of how Hiro works.

### Option 4: Network Namespaces + Transparent Proxy (SNI Inspection)

Each worker spawns in its own network namespace (`CLONE_NEWNET`). The control plane creates a veth pair and runs a transparent proxy that reads TLS SNI / HTTP Host headers to enforce domain policy.

**Pros:**
- Kernel-level isolation per agent
- Transparent to all tools
- Hostname-based filtering via SNI/Host inspection

**Cons:**
- **Fragile: depends on reading application-layer headers.** TLS Encrypted Client Hello (ECH, RFC 9258) encrypts the SNI field, and adoption is growing (Cloudflare has deployed it widely). As ECH becomes standard, SNI inspection becomes unreliable.
- **HTTP/HTTPS only.** Cannot filter SSH, git://, or other TCP protocols — these have no inspectable hostname header. Git-over-SSH is a core developer workflow.
- **Parsing untrusted bytes in root.** The transparent proxy parses TLS ClientHello from agent traffic. Any parsing bug is a root code-execution issue in the control plane.
- Requires `CAP_NET_ADMIN` for veth setup

**Verdict:** Wrong layer. Application-layer inspection is inherently fragile, protocol-specific, and creates unnecessary attack surface. The filtering should happen at the network layer (IP addresses), with the DNS forwarder bridging the gap between human-readable hostnames and machine-enforceable IPs. See Option 7.

### Option 5: Unix Socket HTTP Proxy

Block IP sockets via seccomp. Run an HTTP proxy on a Unix domain socket. Set `HTTP_PROXY` to the socket path.

**Investigated and rejected.** Most CLI tools do not support Unix socket proxy URLs:

| Tool | Unix socket proxy support |
|------|--------------------------|
| curl | No (via env var) |
| git | No |
| pip | No |
| npm | No |
| wget | No |
| apt-get | No |
| Go binaries | No |

The `http+unix://` URL scheme is a convention used by Docker's SDK client and a handful of libraries. It is not a standard. ~0% of general-purpose CLI tools understand it.

**Verdict:** Fundamentally broken. Does not work with the tool ecosystem agents need.

### Option 6: LD_PRELOAD Network Interception

Intercept `connect(2)` via `LD_PRELOAD` and route through a proxy.

**Rejected.** Fails for statically linked binaries and Go binaries (which use raw syscalls, not libc). Trivially bypassable by agents that compile their own binaries or make raw syscalls.

### Option 7: Network Namespaces + DNS-Driven Firewall (Chosen)

Each worker spawns in its own network namespace (`CLONE_NEWNET`). The control plane creates a veth pair and acts as the worker's gateway. A DNS forwarder resolves allowed domains and **dynamically populates nftables IP sets with the resolved addresses**. Filtering is purely at the IP layer — no application-layer protocol inspection.

**Pros:**
- **Protocol-agnostic.** Works for HTTPS, HTTP, SSH, git://, and any TCP protocol. No SNI inspection, no header parsing.
- **Future-proof.** Does not depend on being able to read TLS headers. ECH, TLS 1.4, or any future encryption of application-layer metadata is irrelevant — filtering happens at the IP layer.
- **No untrusted byte parsing.** The DNS forwarder parses standard DNS queries (well-understood, small attack surface). No TLS ClientHello parsing in the control plane.
- **IPs are always fresh.** Populated at DNS resolution time, not periodically refreshed. No stale IP / CDN rotation problem.
- **Kernel-level isolation per agent.** Each agent is in its own network namespace with its own veth and nftables rules.
- **Transparent to all tools.** Worker sees normal `eth0`, default route, DNS. Every CLI tool works unmodified.
- **Simple enforcement model.** DNS forwarder is the gatekeeper; nftables is the enforcer. Two well-understood, battle-tested components.
- **Natural inheritance.** Child agent's network policy intersected with parent's, same as tool capabilities.
- **Clean lifecycle.** Namespace and veth destroyed when worker process exits.
- **Per-agent `/30` subnets** prevent all cross-agent layer-2 attacks by architecture.

**Cons:**
- Requires `CAP_NET_ADMIN` + custom seccomp profile on the container (see [Container Privilege Requirements](#container-privilege-requirements))
- ~5-10ms added to worker spawn time for veth setup (negligible)
- New dependency: `vishvananda/netlink` (same library Docker uses)
- IP-based filtering allows access to any service that shares an IP with an allowed domain (shared hosting/CDN). Low risk — attacker can't control which IP a legitimate domain resolves to.

**Verdict:** Right layer, right primitives. DNS bridges hostnames to IPs. nftables enforces at the IP layer. Protocol-agnostic, future-proof, simple.

## Chosen Design: Network Namespaces + DNS-Driven Firewall

### Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│ Docker Container (CAP_NET_ADMIN + seccomp: allow CLONE_NEWUSER)   │
│                                                                  │
│  Control Plane (root, host network namespace)                    │
│  ├── HTTP API, WebSocket, LLM calls                             │
│  ├── DNS forwarder (resolves allowed domains,                   │
│  │    populates nftables IP sets on resolution)                  │
│  ├── veth host ends (10.0.{id}.1/30 each)                       │
│  ├── IP forwarding + NAT (MASQUERADE for allowed traffic)       │
│  └── nftables: per-agent FORWARD rules with dynamic IP sets     │
│                                                                  │
│  ┌─────────────────────┐  ┌─────────────────────┐               │
│  │ Agent A (netns A)   │  │ Agent B (netns B)   │               │
│  │ UID 10000           │  │ UID 10001           │               │
│  │ eth0: 10.0.0.2/30   │  │ eth0: 10.0.1.2/30   │               │
│  │ gw: 10.0.0.1        │  │ gw: 10.0.1.1        │               │
│  │ dns: 10.0.0.1       │  │ dns: 10.0.1.1        │               │
│  └─────────────────────┘  └─────────────────────┘               │
│                                                                  │
│  gRPC: Unix domain sockets (unaffected by CLONE_NEWNET)         │
└──────────────────────────────────────────────────────────────────┘
```

### How It Works

The design has two components: a **DNS forwarder** that bridges hostnames to IPs, and **nftables rules** that enforce at the IP layer. No application-layer protocol inspection.

#### The DNS-to-Firewall Flow

```
Agent runs: git clone git@github.com:user/repo.git

1. SSH client resolves github.com
   → DNS query to 10.0.0.1 (the gateway)

2. DNS forwarder receives query for "github.com"
   → Checks agent's allowlist: "github.com" ✓
   → Forwards to upstream DNS (Docker's 127.0.0.11)
   → Gets response: 140.82.121.3, 140.82.121.4

3. DNS forwarder adds resolved IPs to agent's nftables set
   → nft add element inet hiro agent_0_ips { 140.82.121.3, 140.82.121.4 }
   → IPs added with TTL matching DNS response TTL

4. DNS forwarder returns response to agent
   → Agent receives: github.com → 140.82.121.3

5. SSH client connects to 140.82.121.3:22
   → Packet hits nftables FORWARD chain
   → Destination 140.82.121.3 is in agent_0_ips set → ACCEPT
   → NAT (MASQUERADE) translates source to container's external IP
   → Connection established

6. Agent tries to connect to 1.2.3.4:443 (hardcoded IP)
   → Packet hits nftables FORWARD chain
   → Destination 1.2.3.4 is NOT in agent_0_ips set → DROP
```

Every protocol works identically. The agent's tools see normal networking — DNS resolves, connections succeed to allowed destinations, connections fail to everything else. No proxy configuration, no special handling per protocol.

### Spawn Protocol Changes

Current:
1. Acquire UID from pool
2. Chown instance directory
3. Fork `hiro agent` with `SysProcAttr.Credential`
4. Worker starts gRPC, writes "ready"

New:
1. Acquire UID from pool
2. Chown instance directory
3. Fork `hiro agent` with `SysProcAttr.Credential` **+ `Cloneflags: CLONE_NEWUSER | CLONE_NEWNET | CLONE_NEWNS`** and UID/GID mapping (maps UID 0 inside → agent UID outside, e.g., `0 10000 1`)
4. **Control plane: create veth pair (temp peer name `hp-{prefix}` in host ns), move peer into child's network namespace, configure host-side address and nftables rules**
5. **Control plane: signal child that veth is ready**
6. **Child (inside its user namespace, where it has full capabilities): configure eth0 address, default route, loopback, bind-mount per-agent `/etc/resolv.conf` and `/etc/hosts`**
7. **Child: install per-worker seccomp-BPF filter (blocks `unshare`, `mount`, `chroot`, `pivot_root`) + set `PR_SET_NO_NEW_PRIVS`, then signal ready**
8. **Control plane: register agent with DNS forwarder, start per-agent listener on gateway IP**
9. Worker starts gRPC, writes "ready"

**Why CLONE_NEWUSER:** The kernel gates `CLONE_NEWNET` and `CLONE_NEWNS` on `CAP_SYS_ADMIN` — the broadest Linux capability. By wrapping namespace creation in a user namespace (`CLONE_NEWUSER`), the child process gets full capabilities *within* its own user namespace without the container needing `CAP_SYS_ADMIN`. The container only needs `CAP_NET_ADMIN` (for veth creation and nftables management from the host namespace) plus a custom seccomp profile that allows `CLONE_NEWUSER` (blocked by Docker's default seccomp). See [Container Privilege Requirements](#container-privilege-requirements).

**UID mapping:** The child's user namespace maps UID 0 inside → agent UID outside (e.g., `0 10000 1` in `uid_map`). This gives the child full capabilities within its namespace for configuring interfaces and bind mounts, while the host sees the process as the non-root agent UID. Go's `SysProcAttr.UidMappings`/`GidMappings` writes these mappings between `clone()` and `exec()`, so the child starts with the correct mapping already in place.

**Self-configuration model:** The parent cannot configure interfaces inside the child's network namespace — `nsenter --net` enters the netns but not the userns, so operations fail with EPERM. Instead, the child configures itself from inside its user namespace where it has full capabilities. The parent's role is limited to: (a) creating the veth pair in the host namespace, (b) moving the peer into the child's netns, and (c) managing nftables rules. This is actually a cleaner separation of concerns than the original design where the parent entered the child's namespaces.

**Race condition analysis:** `CLONE_NEWNET` creates the worker in an empty network namespace — no interfaces, no routes, no connectivity. The worker has zero network access between fork (step 3) and veth setup (steps 4-6). nftables rules are configured in step 4, before the child brings up its interface. There is no window of unrestricted access.

**Error handling:** If veth setup or child self-configuration fails, the worker process must be killed and any partial veth state cleaned up. The `WorkerHandle.Close` closure calls `NetIso.Teardown()` which is idempotent. If the worker exits before setup completes, the namespace is destroyed with the process — veth creation targeting a dead namespace returns an error that propagates to the caller.

**Implementation note — veth peer naming:** The veth peer cannot be created with the name `eth0` in the host namespace (conflicts with the container's own `eth0`). Instead, a temporary name (`hp-{sessPrefix}`) is used. After the parent moves the peer into the child's netns, the child renames it to `eth0`.

### nftables Rules

The control plane creates a single nftables table (`inet hiro`) with per-agent chains and dynamic IP sets. Rules are applied on the host-side FORWARD chain (traffic transiting between agent veth and the container's external interface).

```
table inet hiro {
    # Per-agent dynamic IP set (populated by DNS forwarder)
    set agent_0_ips {
        type ipv4_addr
        flags timeout    # entries expire based on DNS TTL
    }

    # Per-agent FORWARD chain
    chain agent_0 {
        # Allow established connections
        ct state established,related accept

        # Allow DNS to gateway only (UDP + TCP)
        ip daddr 10.0.0.1 meta l4proto { tcp, udp } th dport 53 accept

        # Allow traffic to DNS-resolved IPs (any port, any protocol)
        ip daddr @agent_0_ips accept

        # Default deny
        counter drop
    }

    # Main FORWARD chain
    chain forward {
        type filter hook forward priority 0; policy drop;

        # Route agent traffic to per-agent chain based on source subnet
        ip saddr 10.0.0.2/30 jump agent_0
        ip saddr 10.0.1.2/30 jump agent_1
        # ... one rule per agent
    }
}
```

Key properties:
- **Default deny.** No entries in the IP set → no connectivity (except DNS to the gateway).
- **Any port, any protocol.** Once an IP is in the set, the agent can reach it on any port. SSH, HTTPS, HTTP, git:// — all work.
- **TTL-based expiry.** IP set entries expire based on the DNS TTL (with a minimum floor of 30 seconds to reduce churn for short-TTL CDN domains). Agent must re-resolve to maintain access. Stale IPs are automatically cleaned up. Note: `ct state established,related accept` ensures that active connections survive TTL expiry — only new connections to expired IPs are blocked. This is standard stateful firewall behavior.
- **No application-layer inspection.** nftables sees only IP addresses and ports. No TLS parsing, no HTTP headers, no SNI.
- **Session-scoped naming.** nftables chains and sets use session ID prefixes (e.g., `agent_{session_prefix}_ips`) rather than sequential indices, matching the existing socket naming pattern in `spawn.go`. This prevents any namespace reuse issues.

#### Agents with No Network Policy

Agents without a `network` field in their definition get an empty IP set and no DNS forwarding. The nftables chain allows only DNS (which returns NXDOMAIN for everything) and drops all other traffic. Effectively zero connectivity.

#### Agents with `egress: ["*"]`

Wildcard agents get a permissive FORWARD chain that allows all traffic (with NAT). DNS queries are forwarded without filtering. Any agent definition can declare `egress: ["*"]`, but the inheritance model constrains it — the effective policy is always intersected with the parent's. This is consistent with how tool capabilities work: any agent can declare `allowed_tools: [Bash]`, but it only takes effect if the parent also allows Bash. The operator (root of the agent tree) is the gatekeeper.

### DNS Forwarder

The DNS forwarder is the core of the design. It runs in the control plane, listening on each agent's gateway IP (e.g., `10.0.0.1:53`). It has three responsibilities:

1. **Domain filtering:** Check if the queried domain matches the agent's `network.egress` allowlist (exact match or wildcard). Return NXDOMAIN for disallowed domains.

2. **IP set population:** When a query is allowed, forward to upstream DNS, then add all resolved IPs to the agent's nftables set with a TTL matching the DNS response TTL. **The nftables set write must complete before the DNS response is returned to the agent.** If the set write fails, return SERVFAIL (which triggers DNS retry logic in most resolvers) rather than returning IPs the agent can't reach. Resolved IPs must be filtered before insertion — reject RFC-1918, loopback, link-local, multicast, and unspecified addresses to prevent DNS rebinding attacks.

3. **CNAME handling:** DNS responses may include CNAME chains (e.g., `github.com → github.com.cdn.fastly.net → 151.101.1.194`). The forwarder must collect all terminal A/AAAA records from the full answer section, regardless of intermediate CNAMEs. CNAME targets are not validated against the allowlist — only the originally queried domain is checked. Implement a CNAME depth limit (8, matching Go's resolver).

4. **Transport:** The forwarder must listen on both UDP/53 and TCP/53. DNS over TCP is required for DNSSEC-signed zones, domains with many records, and truncated UDP responses (TC bit). The Go `miekg/dns` library supports both with the same handler.

5. **Logging:** Log all queries per agent (domain, resolved IPs, allowed/denied) for audit.

#### Wildcard Domain Matching

`*.github.com` in the allowlist matches `api.github.com`, `raw.githubusercontent.com` (no — that's a different domain), etc. The matching rules:

- `github.com` matches exactly `github.com`
- `*.github.com` matches any subdomain: `api.github.com`, `foo.bar.github.com`, etc. Does NOT match `github.com` itself.
- To allow both, declare both: `github.com` and `*.github.com`

#### DNS Bypass Prevention

- **Hardcoded IPs:** Blocked. The IP is not in the nftables set (it was never resolved through the forwarder).
- **Alternative DNS resolvers:** Blocked. nftables only allows DNS (port 53) to the gateway IP. The agent cannot reach `8.8.8.8:53` or any other resolver.
- **DNS-over-HTTPS:** Blocked. The DoH endpoint IP was never resolved through the forwarder (unless the DoH domain is in the allowlist). Even if the agent resolves an allowed domain and uses it for DoH, the resolved IPs for the *queried* domain are not added to the nftables set — only the forwarder adds IPs.
- **DNS tunneling:** The forwarder only forwards queries for allowed domains. Queries to attacker-controlled domains get NXDOMAIN.

### Agent Configuration

```yaml
---
name: code-agent
allowed_tools: [Bash, Read, Write, Edit, Glob, Grep, WebFetch]
network:
  egress:
    - "github.com"
    - "*.github.com"
    - "*.npmjs.org"
    - "pypi.org"
    - "*.pypi.org"
```

- **No `network` field**: No outbound connectivity. Agent can only use Unix sockets (gRPC to control plane). This is the default — the opposite of every other agent platform, which grants unrestricted network by default.
- **`egress: ["*"]`**: Unrestricted outbound. Constrained by inheritance — a child declaring `["*"]` only gets what its parent allows.
- **Specific domains**: DNS-filtered and firewall-enforced. Works for any protocol.

#### Network Policy Inheritance

Network policy inherits through the agent hierarchy using intersection semantics, identical to tool capabilities:

```
child effective egress = child declared egress ∩ parent effective egress
```

Concrete examples:
- Parent `["*"]` ∩ child `["github.com"]` = `["github.com"]` — child is constrained
- Parent `["github.com"]` ∩ child `["*"]` = `["github.com"]` — child cannot exceed parent
- Parent `["github.com", "pypi.org"]` ∩ child `["github.com"]` = `["github.com"]`
- Parent `["github.com"]` ∩ child `["npmjs.org"]` = `[]` — no overlap, child has no network

A child agent can never have broader network access than its parent.

### Namespace Filesystem Setup

`CLONE_NEWNET` creates a new network namespace but does NOT clone the filesystem. The agent sees the container's `/etc/resolv.conf` and `/etc/hosts`. Both must be controlled per-agent to prevent DNS forwarder bypass.

**Required bind mounts per agent (set up during veth configuration, step 4 of spawn):**

```
/etc/resolv.conf → agent-specific file:
    nameserver 10.0.{id}.1
    search .
    options ndots:1

/etc/hosts → minimal file:
    127.0.0.1 localhost
    ::1       localhost
```

The child process bind-mounts these from inside its own user+mount namespace (where it has `CAP_SYS_ADMIN` scoped to the namespace). The spawn uses `CLONE_NEWNS` (in addition to `CLONE_NEWUSER` and `CLONE_NEWNET`) to give each agent a private mount namespace where these files can be overridden without affecting other agents or the control plane. The source files are written by the control plane to a temp directory; the child reads and bind-mounts them during its self-configuration phase (step 6 of spawn).

**Additional mitigations:**
- Block mDNS (UDP port 5353 to `224.0.0.251`) in the per-agent nftables chain
- Disable IPv6 in agent namespaces: `sysctl -w net.ipv6.conf.eth0.disable_ipv6=1` during veth setup. This eliminates IPv6 bypass of the IPv4-only nftables sets and reduces attack surface.

### WebFetch Interaction

**WebFetch stays in the worker.** Its network access is governed by the same `network.egress` policy as Bash commands — the DNS forwarder and nftables rules apply equally to all traffic from the worker's network namespace.

- Agent with `network.egress` configured: `WebFetch` works for allowed domains, same as `curl`
- Agent without `network.egress`: `WebFetch` fails (no outbound connectivity)
- The existing `ssrfTransport` (blocks RFC-1918, loopback, link-local) provides additional defense-in-depth for WebFetch specifically

This preserves the isolation boundary: the control plane never makes HTTP requests on behalf of agents.

### Per-Worker seccomp (Required)

Per-worker seccomp-BPF is **mandatory** for the `CLONE_NEWUSER` approach — not optional hardening. The container-wide seccomp profile allows `CLONE_NEWUSER`, `unshare`, and `mount` so the control plane can create namespaces and the child can self-configure. Without a per-worker filter, agents inherit these permissions and can create their own user namespaces, gaining `CAP_SYS_ADMIN` within them — the exact kernel attack surface Docker blocks `CLONE_NEWUSER` to prevent.

**Critical finding from prototyping:** `PR_SET_NO_NEW_PRIVS` does NOT block `CLONE_NEWUSER`. The kernel allows unprivileged user namespace creation regardless of the no-new-privs flag. Only seccomp-BPF can block it.

**Per-worker seccomp-BPF filter (applied in `runAgent()` before any agent code):**

The filter blocks these syscalls with `SECCOMP_RET_ERRNO(EPERM)`:
- `unshare` — prevents creating new namespaces (blocks `CLONE_NEWUSER` escalation)
- `mount`, `umount` — prevents filesystem manipulation
- `chroot`, `pivot_root` — prevents root filesystem escapes
- `ptrace` — prevents debugging other processes
- `kexec_load`, `reboot`, `swapon`, `keyctl` — misc dangerous syscalls

All other syscalls are allowed (`SECCOMP_RET_ALLOW`). The filter is installed after `PR_SET_NO_NEW_PRIVS` (required by the kernel before installing seccomp-BPF) and before `exec` of the worker binary.

**Defense-in-depth (optional, not yet planned):** Block `socket(AF_INET)` and `socket(AF_INET6)` as a backstop. If an agent escapes its network namespace via a kernel exploit, seccomp prevents it from creating new IP sockets. This is redundant with namespace isolation in the normal case, but provides a second barrier.

A prototype (`proto/netiso-userns/drop_privs.c`) validates this approach: a small C program installs the seccomp-BPF filter then execs the command. The prototype's test 7 demonstrates the full lifecycle — child creates namespaces, self-configures interfaces and bind mounts, then the seccomp filter is installed. After that, the agent can use the network and read/write files but cannot `unshare`, `mount`, or `chroot`.

**Container-wide seccomp profile (Docker):**
- **Required for network isolation:** Custom profile extending Docker defaults to allow `clone` (with `CLONE_NEWUSER`), `unshare`, and `setns` — needed for the control plane to create user namespaces
- Remove `io_uring_*` (persistent source of kernel CVEs)
- Remove `chroot`, `pivot_root` (not needed at the container level)
- Applied via `security_opt: [seccomp=seccomp.json]` in docker-compose

### Container Privilege Requirements

The original design anticipated needing only `CAP_NET_ADMIN`. The initial implementation used `CAP_SYS_ADMIN` because `CLONE_NEWNET` and `CLONE_NEWNS` are gated on it by the kernel. However, prototyping revealed that `CLONE_NEWUSER` (user namespaces) eliminates the need for `CAP_SYS_ADMIN` entirely — the child process creates its own network and mount namespaces from within a user namespace where it has full capabilities.

| Requirement | Why | Docker Config |
|---|---|---|
| `CAP_NET_ADMIN` | nftables rules, veth pair creation/movement in host namespace | `cap_add: [NET_ADMIN]` |
| Custom seccomp profile | Allow `CLONE_NEWUSER` (blocked by Docker's default seccomp) | `security_opt: [seccomp=seccomp.json]` |
| `net.ipv4.ip_forward=1` | Route traffic between agent veths and container's external interface | `sysctls: [net.ipv4.ip_forward=1]` |

**What was NOT needed:**
- **No `CAP_SYS_ADMIN`** — `CLONE_NEWUSER` wraps `CLONE_NEWNET` + `CLONE_NEWNS`, giving the child full capabilities within its own user namespace without granting `CAP_SYS_ADMIN` to the container
- No `--privileged` flag
- Integration tests (`make test-netiso`) use a Docker Compose file (`docker-compose.test-netiso.yml`) with the exact same privileges as production

**The custom seccomp profile** extends Docker's default to allow three additional syscalls: `clone` (with `CLONE_NEWUSER` flag), `unshare`, and `setns`. Docker's default blocks `CLONE_NEWUSER` because unprivileged user namespaces have historically been a source of kernel privilege escalation bugs. The profile is minimal — it does not open `CAP_SYS_ADMIN`-gated operations.

**The `net.ipv4.ip_forward` sysctl** is per-container-namespace (does not affect the host). It is read-only at runtime in non-privileged containers, so it must be set at container creation time via `docker-compose.yml`. The code gracefully handles this — if the write fails, it checks whether forwarding is already enabled.

#### Why CLONE_NEWUSER Over CAP_SYS_ADMIN

The initial implementation used `CAP_SYS_ADMIN` to create namespaces from the control plane. This worked but granted the broadest Linux capability to the container — mount operations, cgroup manipulation, device management, and much more. A shell script prototype (`proto/netiso-userns/`) validated the alternative approach:

1. **Child creates its own namespaces** via `CLONE_NEWUSER | CLONE_NEWNET | CLONE_NEWNS` — no `CAP_SYS_ADMIN` needed
2. **Parent creates/moves veth** with `CAP_NET_ADMIN` — this works across the user namespace boundary
3. **Child self-configures** interfaces, routes, and bind mounts from inside its user namespace (where it has full capabilities)
4. **Parent manages nftables** in the host namespace with `CAP_NET_ADMIN`

The tradeoff is a custom seccomp profile (allowing `CLONE_NEWUSER`) instead of `CAP_SYS_ADMIN`. This is a strictly better security posture: `CLONE_NEWUSER` gives the child capabilities only within its own namespace (which the container already controls), while `CAP_SYS_ADMIN` gives the container capabilities over the entire system namespace.

**Seccomp risk note:** Unprivileged user namespaces (`CLONE_NEWUSER`) have historically been a vector for kernel privilege escalation (the child gets `CAP_SYS_ADMIN` *within* its user namespace, which can be used to exercise kernel code paths normally restricted to privileged users). The container-wide seccomp profile allows `CLONE_NEWUSER` for the control plane, but the **per-worker seccomp-BPF filter** (see [Per-Worker seccomp](#per-worker-seccomp-required)) blocks agents from creating their own user namespaces. This is critical: `PR_SET_NO_NEW_PRIVS` alone does NOT prevent `CLONE_NEWUSER` — only seccomp-BPF can block it. The combination of per-worker seccomp + non-root agent UIDs + Docker's outer seccomp profile provides defense-in-depth. The risk is materially lower than granting `CAP_SYS_ADMIN` to the container directly.

#### Capability Risk Assessment

**CAP_NET_ADMIN grants (to root in the container):**
- Modify nftables rules (including the isolation rules this design depends on)
- Configure network interfaces, routes, tunnels
- Set socket options like `SO_BINDTODEVICE`

This is the only Linux capability added beyond Docker's defaults. It is well-scoped to network administration — no filesystem, mount, or namespace operations.

**What agents can do with these capabilities:** Agent workers run as non-root UIDs in the host namespace with zero capabilities there. Within their user namespace, agents initially have full capabilities for self-configuration (step 6 of spawn), but the per-worker seccomp-BPF filter (step 7) blocks the dangerous operations (`unshare`, `mount`, `chroot`, `pivot_root`) before any agent code runs. After seccomp is installed, agents cannot create new namespaces, mount filesystems, or perform any operation that would leverage their in-namespace capabilities for escalation.

**The actual risk:** If an agent achieves control-plane code execution (via a bug in gRPC handling, tool dispatching, or DNS parsing), it inherits `CAP_NET_ADMIN` and could modify nftables rules or network configuration. This is a meaningful but bounded risk — it cannot mount host filesystems, manipulate cgroups, or perform the broader set of operations that `CAP_SYS_ADMIN` would have enabled.

### Known Limitations

- **IP address overlap.** If an allowed domain (e.g., `github.com`) shares an IP with a blocked domain (e.g., `evil.com` on the same CDN), the agent can reach `evil.com` at that IP. This is inherent to IP-layer filtering. Low risk: the attacker cannot control which IP a legitimate domain resolves to, and shared-IP hosting is increasingly rare for high-value targets. This is the same limitation as any traditional firewall.
- **Shared filesystem covert channels.** Agents could theoretically communicate via files in `/tmp` or the shared workspace. Mitigated by `0700` instance dirs and per-agent `TMPDIR`. Low bandwidth, low risk.
- **Ingress to agents.** Agents can't receive inbound connections (no port exposure). Not currently a requirement.
- **DNS TTL accuracy.** If a domain's IPs change faster than the DNS TTL, agents may briefly be able to reach old IPs or be unable to reach new IPs. In practice, DNS TTLs are set by domain operators to account for propagation delays, so this window is small.
- **Non-DNS name resolution.** Agents that use `/etc/hosts` or mDNS to resolve names bypass the DNS forwarder. See [Namespace Filesystem Setup](#namespace-filesystem-setup) for required mitigations.

## Resolved Questions

1. **`egress: ["*"]` semantics:** Any agent can declare it. Inheritance constrains it — `child effective = child declared ∩ parent effective`. This is consistent with tool capabilities. The operator is the gatekeeper. No special-casing needed.

2. **WebFetch for no-network agents:** Agents without `network.egress` can't use WebFetch. This is acceptable and intentional. If an agent needs to fetch URLs, it declares the domains in `network.egress`. A "WebFetch works but curl doesn't" split would be confusing — one consistent model where network access is network access.

3. **IPv6:** Disabled entirely in agent namespaces. Simplest, safest. Eliminates IPv6 bypass of IPv4-only nftables sets. See [Namespace Filesystem Setup](#namespace-filesystem-setup).

4. **nftables set size limits.** No hard limit. nftables hash sets handle thousands of entries with O(1) lookup. TTL-based expiry naturally bounds set size. If an agent resolves an unusually large number of domains, the IPs expire based on DNS TTL. Not a practical concern.

## Security Reviews

This design went through two rounds of review: first against the transparent proxy design (Option 4), then against the DNS-driven firewall (Option 7). The second review confirmed the architectural shift was correct and found no critical issues.

### Review 1: Transparent Proxy Design (Option 4)

These findings drove the redesign from transparent proxy to DNS-driven firewall:

| ID | Severity | Finding | Resolution |
|---|---|---|---|
| R1-01 | Critical | WebFetch migration to control plane creates SSRF risk | Keep WebFetch in worker |
| R1-02 | High | SNI bypass via ECH or missing SNI | Eliminated: design no longer inspects SNI |
| R1-03 | High | No default-deny for non-HTTP ports (SSH, raw TCP bypass proxy) | Eliminated: DNS-driven approach is protocol-agnostic |
| R1-04 | High | TLS ClientHello parser runs as root in control plane | Eliminated: no transparent proxy, DNS parsing is much smaller surface |
| R1-05 | Medium | CAP_NET_ADMIN blast radius | Accepted: control-plane compromise is already game-over |
| R1-06 | Medium | Spawn error handling for partial veth setup | Documented cleanup requirements |

### Review 2: DNS-Driven Firewall Design (Option 7)

No critical findings. All high/medium findings are implementation requirements, not design flaws:

| ID | Severity | Finding | Resolution |
|---|---|---|---|
| R2-01 | High | DNS response must be withheld until nftables set write succeeds | Implementation req: synchronous write, SERVFAIL on failure |
| R2-02 | High | DNS forwarder must filter private IPs before adding to nftables set | Implementation req: reject RFC-1918/loopback/link-local |
| R2-03 | Medium | TTL expiry can break connection pools for short-TTL domains | 30s minimum TTL floor; `ct state established` protects active connections |
| R2-04 | Medium | `/etc/resolv.conf` and `/etc/hosts` must be controlled per-namespace | Implementation req: bind mounts via CLONE_NEWNS |
| R2-05 | Medium | CNAME chains: collect terminal A/AAAA records, don't validate CNAME targets | Implementation req: traverse full answer section |
| R2-06 | Medium | DNS forwarder must support TCP/53 alongside UDP/53 | Implementation req: ~10 lines with miekg/dns |
| R2-07 | Medium | `egress: ["*"]` scoping | Resolved: consistent with tool capability model, inheritance constrains |
| R2-08 | Low | nftables set updates are atomic (RCU) — confirmed safe | No action needed |
| R2-09 | Low | Use session ID prefix for nftables naming, not sequential index | Implementation req: matches existing socket naming |
| R2-10 | Low | Verify `net.DefaultResolver.LookupHost` respects namespace `/etc/resolv.conf` | Verify during implementation |

### Why DNS-Driven Firewall Over Transparent Proxy

The shift from Option 4 to Option 7 eliminated three high-severity findings from Review 1 and introduced zero new critical/high design issues:

- **R1-02 (SNI bypass):** No longer applicable. The design does not inspect SNI or any TLS header.
- **R1-04 (parser in root):** The DNS forwarder parses standard DNS queries — a much smaller, better-understood attack surface than TLS ClientHello.
- **R1-03 (non-HTTP ports):** Fully resolved. SSH, git://, and any TCP protocol work identically to HTTPS.

The remaining findings (R2-01 through R2-10) are implementation correctness requirements, not architectural concerns.

## Implementation Notes

Discoveries and deviations from the original design, documented during implementation.

### Privilege Model Evolution

The original design stated "Requires `CAP_NET_ADMIN` on the container" as the sole privilege requirement. The initial implementation discovered that `CLONE_NEWNET` and `CLONE_NEWNS` are gated on `CAP_SYS_ADMIN`, not `CAP_NET_ADMIN`, and used `CAP_SYS_ADMIN` as a workaround.

A subsequent prototype (`proto/netiso-userns/`) validated that `CLONE_NEWUSER` eliminates the `CAP_SYS_ADMIN` requirement entirely. The final privilege set is `CAP_NET_ADMIN` + custom seccomp (allowing `CLONE_NEWUSER`) + `net.ipv4.ip_forward=1` sysctl. See [Container Privilege Requirements](#container-privilege-requirements).

The key architectural change: the control plane no longer enters the child's namespaces to configure them. Instead, the child creates its own `NEWUSER | NEWNET | NEWNS` namespaces and self-configures (interfaces, routes, bind mounts) from inside its user namespace where it has full capabilities. The control plane only operates in the host namespace: creating veth pairs, moving peers into the child's netns, and managing nftables rules.

### Implementation Discoveries

1. **`net.ipv4.ip_forward=1` sysctl.** Cannot be set at runtime in a non-privileged container — `/proc/sys/net/ipv4/ip_forward` is read-only. Must be declared in `docker-compose.yml` via `sysctls: [net.ipv4.ip_forward=1]`. The code handles this gracefully (checks if already enabled before failing).

2. **Child self-configuration via user namespace.** The parent cannot `nsenter --net` into the child's network namespace to configure interfaces — entering the netns without also entering the userns means the parent lacks capabilities within the child's namespace (EPERM). The child must configure its own interfaces, routes, and bind mounts. This is enforced by a handshake protocol: parent creates veth and signals, child configures and signals back.

3. **UID mapping for user namespaces.** `CLONE_NEWUSER` without explicit UID/GID mapping results in the child having zero capabilities (mapped to `nobody`). The child needs a mapping (e.g., `0 10000 1` — UID 0 inside maps to agent UID 10000 outside) to get full capabilities within its namespace. In Go, this is set via `SysProcAttr.UidMappings`/`GidMappings`, which writes `uid_map`/`gid_map` between `clone()` and `exec()`. Shell's `unshare --map-root-user` maps `0→0` (caller's UID); the prototype validated that the kernel accepts non-root outer UIDs via direct `uid_map` writes.

4. **`PR_SET_NO_NEW_PRIVS` does NOT block `CLONE_NEWUSER`.** This was initially assumed to be sufficient for preventing agents from creating user namespaces. Prototyping proved otherwise — the kernel allows `unshare(CLONE_NEWUSER)` regardless of the no-new-privs flag. Per-worker seccomp-BPF is the only mechanism that reliably blocks it. The prototype's `drop_privs.c` validates this: a BPF filter that returns `SECCOMP_RET_ERRNO(EPERM)` for `unshare` successfully blocks namespace creation while allowing all normal operations.

5. **Veth peer naming.** The design implied creating the peer as `eth0` directly. This fails because the peer is initially created in the host namespace (where `eth0` already exists). Solution: create with a temporary name (`hp-{prefix}`), move into agent namespace; child renames to `eth0`.

6. **Integration test privileges.** Tests use `docker-compose.test-netiso.yml` with the exact same capabilities, seccomp profile, and sysctls as production — no `--privileged`, no special treatment. This ensures the tests validate the actual production security boundary.

### DNS Upstream Detection

The design hardcoded Docker's embedded DNS at `127.0.0.11:53`. This works in Docker Compose but not in standalone `docker run` (where the container gets the host's DNS configuration). The implementation reads `/etc/resolv.conf` to find the system nameserver, which works in both environments.

### nftables Rule Handle Lifecycle

The `google/nftables` library's `AddRule` returns a rule object, but the kernel-assigned handle is not populated until after `Flush()` — and even then, it isn't backfilled into the Go object. Teardown cannot use `DelRule` with the original object. Solution: `GetRules` from the kernel, find the jump rule by matching the verdict chain name, then `DelRule` with the kernel-provided handle.

### Package Structure

All Linux-specific code (netlink, nftables, namespace operations) is gated with `//go:build linux`. Non-Linux platforms get stubs where `Probe()` returns an error and `New()` is unavailable. The `NetIso` struct itself is defined per-platform (not shared) to avoid unused-field lint errors. Cross-platform code (domain matching, IP filtering, types) has no build tags.

## References

- Wang et al. (2026). "A Systematic Security Evaluation of OpenClaw and Its Variants." arXiv:2604.03131.
- Liu et al. (2026). "ClawKeeper: Comprehensive Safety Protection for OpenClaw Agents Through Skills, Plugins, and Watchers." arXiv:2603.24414.
- Docker default seccomp profile: https://docs.docker.com/engine/security/seccomp/
- vishvananda/netlink: https://github.com/vishvananda/netlink (Go netlink library, used by Docker)

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
- Requires `CAP_NET_ADMIN` on the container (see [CAP_NET_ADMIN Risk Assessment](#cap_net_admin-risk-assessment))
- ~5-10ms added to worker spawn time for veth setup (negligible)
- New dependency: `vishvananda/netlink` (same library Docker uses)
- IP-based filtering allows access to any service that shares an IP with an allowed domain (shared hosting/CDN). Low risk — attacker can't control which IP a legitimate domain resolves to.

**Verdict:** Right layer, right primitives. DNS bridges hostnames to IPs. nftables enforces at the IP layer. Protocol-agnostic, future-proof, simple.

## Chosen Design: Network Namespaces + DNS-Driven Firewall

### Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│ Docker Container (CAP_NET_ADMIN)                                 │
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
3. Fork `hiro agent` with `SysProcAttr.Credential` **+ `Cloneflags: CLONE_NEWNET`**
4. **Create veth pair, move worker end into new netns, configure addresses/routes/nftables**
5. Worker starts gRPC, writes "ready"

**Race condition analysis:** `CLONE_NEWNET` creates the worker in an empty network namespace — no interfaces, no routes, no connectivity. The worker has zero network access between fork (step 3) and veth setup (step 4). nftables rules are configured as part of step 4, before any traffic can flow. There is no window of unrestricted access.

**Error handling:** If veth setup fails after the worker is forked, the worker process must be killed and any partial veth state cleaned up. The existing `WorkerHandle.Close` pattern should be extended to handle this. If the worker exits before veth setup completes, the namespace is destroyed with the process — veth creation targeting a dead namespace must be handled gracefully.

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

These are bind-mounted into the agent's mount namespace. The spawn must use `CLONE_NEWNS` (in addition to `CLONE_NEWNET`) to give each agent a private mount namespace where these files can be overridden without affecting other agents or the control plane.

**Additional mitigations:**
- Block mDNS (UDP port 5353 to `224.0.0.251`) in the per-agent nftables chain
- Disable IPv6 in agent namespaces: `sysctl -w net.ipv6.conf.eth0.disable_ipv6=1` during veth setup. This eliminates IPv6 bypass of the IPv4-only nftables sets and reduces attack surface.

### WebFetch Interaction

**WebFetch stays in the worker.** Its network access is governed by the same `network.egress` policy as Bash commands — the DNS forwarder and nftables rules apply equally to all traffic from the worker's network namespace.

- Agent with `network.egress` configured: `WebFetch` works for allowed domains, same as `curl`
- Agent without `network.egress`: `WebFetch` fails (no outbound connectivity)
- The existing `ssrfTransport` (blocks RFC-1918, loopback, link-local) provides additional defense-in-depth for WebFetch specifically

This preserves the isolation boundary: the control plane never makes HTTP requests on behalf of agents.

### seccomp Hardening (Planned, Not Yet Implemented)

Alongside network namespaces, workers should get additional seccomp hardening. This is complementary work, tracked separately from network isolation.

**Per-worker (to be applied in `runAgent()`):**
- `PR_SET_NO_NEW_PRIVS` — prevents setuid escalation, inherited by all child processes. This is the single highest-value change (~3 lines of Go).
- Block dangerous syscalls: `ptrace`, `mount`, `umount`, `kexec_load`, `reboot`, `swapon`, `keyctl`
- **Defense-in-depth:** Block `socket(AF_INET)` and `socket(AF_INET6)` as a backstop. If an agent escapes its network namespace via a kernel exploit, seccomp prevents it from creating new IP sockets. This is redundant with namespace isolation in the normal case, but provides a second barrier for the escalation case.

**Container-wide (Docker seccomp profile JSON):**
- Tighten beyond Docker defaults: block `io_uring_*`, additional kernel-level attack surface reduction
- Applied via `security_opt: seccomp:seccomp-profile.json` in docker-compose

### CAP_NET_ADMIN Risk Assessment

Adding `CAP_NET_ADMIN` to the container is necessary for creating network namespaces and veth pairs. The risk is bounded but should be understood:

**What CAP_NET_ADMIN grants (to root in the container):**
- Create/delete network namespaces and veth pairs
- Modify nftables rules (including the isolation rules this design depends on)
- Configure tunnels (GRE, IPIP, WireGuard kernel module if present)
- Set socket options like `SO_BINDTODEVICE`

**What agents (non-root) can do with CAP_NET_ADMIN:** Nothing. The capability is only available to the root process. Agents run as UIDs 10000-10063 without capabilities.

**The actual risk:** If an agent achieves control-plane code execution (via a bug in gRPC handling, tool dispatching, or DNS parsing), it inherits `CAP_NET_ADMIN`. This means a control-plane compromise now has wider network manipulation capability than without the capability.

**Assessment:** Accept permanently. Control-plane compromise is already game-over for the container regardless of `CAP_NET_ADMIN` — root inside the container can do almost anything. The marginal risk of adding this capability is small compared to the security improvement from per-agent network isolation. Document in the threat model.

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

## References

- Wang et al. (2026). "A Systematic Security Evaluation of OpenClaw and Its Variants." arXiv:2604.03131.
- Liu et al. (2026). "ClawKeeper: Comprehensive Safety Protection for OpenClaw Agents Through Skills, Plugins, and Watchers." arXiv:2603.24414.
- Docker default seccomp profile: https://docs.docker.com/engine/security/seccomp/
- vishvananda/netlink: https://github.com/vishvananda/netlink (Go netlink library, used by Docker)

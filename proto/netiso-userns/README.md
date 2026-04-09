# Network Isolation Prototype

Shell prototype validating per-agent network isolation using Linux user namespaces (`CLONE_NEWUSER`). This approach eliminates the need for `CAP_SYS_ADMIN` — the container only requires `CAP_NET_ADMIN` and a custom seccomp profile.

See [docs/network-isolation.md](../../docs/network-isolation.md) for the full design.

## Run

```bash
docker compose up --build
```

Expects Docker with BuildKit. Runs 38 tests, exits 0 on success.

## What it validates

| Test | What |
|------|------|
| 1 | `CLONE_NEWUSER` grants full capabilities inside the user namespace |
| 2 | Kernel accepts non-root UID mapping (`0 → 10000`) for `SysProcAttr.UidMappings` |
| 3 | Full spawn flow: parent creates veth, child self-configures interfaces + bind mount |
| 4 | Bidirectional connectivity over veth pair |
| 5 | nftables table/chain/set/rule lifecycle with `CAP_NET_ADMIN` only |
| 6 | Per-worker seccomp blocks all escape vectors (see below) |
| 7 | Full lifecycle: namespace setup with full caps, then seccomp installed, then agent code cannot escalate but can still use network and files |

### Seccomp coverage (Test 6)

The per-worker BPF filter blocks:

- `clone(CLONE_NEWUSER)` — BPF argument inspection on clone flags
- `clone(CLONE_NEWNET)` — same mechanism
- `clone3` — blocked unconditionally (Go runtime uses `clone`, not `clone3`)
- `unshare` — prevents namespace creation via the other syscall path
- `setns` — prevents entering other agents' namespaces
- `mount` — prevents bind mount manipulation
- `umount2` — prevents stripping controlled `/etc/resolv.conf`

The container-wide seccomp profile (`seccomp.json`) additionally removes `chroot`, `pivot_root`, `ptrace`, and `keyctl` from Docker's default allowlist.

Normal operations (fork, exec, file I/O, networking) are unaffected.

## Files

| File | Purpose |
|------|---------|
| `test.sh` | Test script (38 assertions) |
| `drop_privs.c` | Per-worker seccomp-BPF filter — installs filter then exec's agent command |
| `try_clone_newuser.c` | Adversarial test: calls `clone(CLONE_NEWUSER)` directly via `clone(2)` |
| `try_setns.c` | Adversarial test: calls `setns(2)` to enter a namespace |
| `seccomp.json` | Container-wide Docker seccomp profile (extends defaults, allows `CLONE_NEWUSER`) |
| `Dockerfile` | Build: Ubuntu 24.04 + iproute2/nftables/gcc, compiles C binaries |
| `docker-compose.yml` | Run config: `CAP_NET_ADMIN`, custom seccomp, `ip_forward=1` |

## Container privileges

```yaml
cap_add: [NET_ADMIN]           # veth pairs + nftables
security_opt:
  - seccomp=seccomp.json       # allow CLONE_NEWUSER (Docker default blocks it)
sysctls:
  - net.ipv4.ip_forward=1      # route between namespaces
# NOTE: no CAP_SYS_ADMIN
```

## Prototype vs production

This prototype validates the kernel primitives using shell scripts and small C programs. The production Go implementation (`internal/netiso/`) differs in:

- **Seccomp timing**: Production installs the BPF filter in `runAgent()` after network self-configuration but before gRPC starts. The prototype uses `exec(drop_privs)` which models the effect but not the timing.
- **UID mapping**: Production uses `SysProcAttr.UidMappings` to map `0 → agent UID` between `clone()` and `exec()`. The prototype uses `--map-root-user` (maps `0 → 0`) for most tests, with Test 2 verifying the kernel accepts non-root mappings.
- **DNS forwarder**: Production implements the full DNS-to-nftables flow in `internal/netiso/dns.go`. The prototype validates kernel primitives only.

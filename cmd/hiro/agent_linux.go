//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/nchapman/hiro/internal/ipc"
)

// selfConfigureNetwork configures the network inside the child's user+net+mount
// namespaces. Called after the parent has created the veth pair and moved the
// peer into this namespace. The child has full capabilities inside its user
// namespace, so it can configure interfaces, routes, and bind mounts.
func selfConfigureNetwork(cfg ipc.SpawnConfig) error {
	// 1. Bring up loopback.
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return fmt.Errorf("finding lo: %w", err)
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		return fmt.Errorf("bringing up lo: %w", err)
	}

	// 2. Rename the veth peer to eth0.
	peer, err := netlink.LinkByName(cfg.PeerName)
	if err != nil {
		return fmt.Errorf("finding peer %s: %w", cfg.PeerName, err)
	}
	if err := netlink.LinkSetName(peer, "eth0"); err != nil {
		return fmt.Errorf("renaming %s to eth0: %w", cfg.PeerName, err)
	}

	// 3. Configure eth0 with agent IP.
	eth0, err := netlink.LinkByName("eth0")
	if err != nil {
		return fmt.Errorf("finding eth0: %w", err)
	}
	agentIP := net.ParseIP(cfg.AgentIP)
	if agentIP == nil {
		return fmt.Errorf("invalid agent IP: %s", cfg.AgentIP)
	}
	mask := net.CIDRMask(cfg.SubnetBits, 32)
	if err := netlink.AddrAdd(eth0, &netlink.Addr{
		IPNet: &net.IPNet{IP: agentIP, Mask: mask},
	}); err != nil {
		return fmt.Errorf("adding address to eth0: %w", err)
	}
	if err := netlink.LinkSetUp(eth0); err != nil {
		return fmt.Errorf("bringing up eth0: %w", err)
	}

	// 4. Add default route via gateway.
	gwIP := net.ParseIP(cfg.GatewayIP)
	if gwIP == nil {
		return fmt.Errorf("invalid gateway IP: %s", cfg.GatewayIP)
	}
	if err := netlink.RouteAdd(&netlink.Route{Gw: gwIP}); err != nil {
		return fmt.Errorf("adding default route: %w", err)
	}

	// 5. Disable IPv6.
	_ = os.WriteFile("/proc/sys/net/ipv6/conf/eth0/disable_ipv6", []byte("1"), 0o644)
	_ = os.WriteFile("/proc/sys/net/ipv6/conf/all/disable_ipv6", []byte("1"), 0o644)

	// 6. Bind-mount per-agent /etc/resolv.conf.
	// Use session-unique paths to avoid races between concurrent agent spawns.
	sessPrefix := cfg.SessionID
	if len(sessPrefix) > 12 {
		sessPrefix = sessPrefix[:12]
	}
	resolvConf := fmt.Sprintf("nameserver %s\nsearch .\noptions ndots:1\n", cfg.GatewayIP)
	resolvPath := fmt.Sprintf("/tmp/hiro-resolv-%s.conf", sessPrefix)
	if err := os.WriteFile(resolvPath, []byte(resolvConf), 0o644); err != nil {
		return fmt.Errorf("writing resolv.conf: %w", err)
	}
	if err := syscall.Mount(resolvPath, "/etc/resolv.conf", "", syscall.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind-mounting resolv.conf: %w", err)
	}
	_ = os.Remove(resolvPath) // inode kept alive by bind mount

	// 7. Bind-mount per-agent /etc/hosts.
	hostsContent := "127.0.0.1 localhost\n::1       localhost\n"
	hostsPath := fmt.Sprintf("/tmp/hiro-hosts-%s", sessPrefix)
	if err := os.WriteFile(hostsPath, []byte(hostsContent), 0o644); err != nil {
		return fmt.Errorf("writing hosts: %w", err)
	}
	if err := syscall.Mount(hostsPath, "/etc/hosts", "", syscall.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind-mounting hosts: %w", err)
	}
	_ = os.Remove(hostsPath) // inode kept alive by bind mount

	return nil
}

// waitForVethReady blocks until the parent signals that the veth peer has been
// moved into this namespace. The signal is the parent closing FD 3 (passed via
// cmd.ExtraFiles).
func waitForVethReady() {
	f := os.NewFile(3, "veth-ready")
	if f == nil {
		return
	}
	buf := make([]byte, 1)
	// Blocks until EOF (parent closes write end after veth setup) or
	// broken pipe (parent died). Either way, proceed with self-configuration —
	// if the parent died, exec.CommandContext cancellation will reap us.
	_, _ = f.Read(buf)
	f.Close()
}

// installSeccomp installs a seccomp-BPF filter that blocks dangerous syscalls.
// This MUST be called after network self-configuration (which needs mount/clone)
// and BEFORE any agent code runs. Uses SECCOMP_FILTER_FLAG_TSYNC to synchronize
// the filter across all Go runtime threads.
//
// Blocked syscalls (EPERM):
//   - clone(CLONE_NEWUSER) — BPF flag inspection prevents user namespace creation
//   - clone(CLONE_NEWNET) — BPF flag inspection prevents network namespace creation
//   - clone3 — blocked unconditionally (Go runtime uses clone, not clone3)
//   - unshare — prevents namespace creation via the other path
//   - setns — prevents entering other agents' namespaces
//   - mount — prevents filesystem manipulation
//   - umount2 — prevents unmounting controlled bind mounts (e.g. /etc/resolv.conf)
//   - ptrace — prevents inspecting/injecting code into other processes
//   - chroot — prevents filesystem root manipulation
//   - pivot_root — prevents filesystem root manipulation
//   - kexec_load — prevents loading a new kernel
func installSeccomp() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// PR_SET_NO_NEW_PRIVS is required before installing a seccomp filter.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", err)
	}

	filter := buildSeccompFilter()
	prog := unix.SockFprog{
		Len:    uint16(len(filter)), //nolint:gosec // filter length fits uint16
		Filter: &filter[0],
	}

	// Use seccomp(2) with TSYNC to apply the filter to all threads.
	// Syscall (not RawSyscall) so the Go scheduler is notified during
	// TSYNC's cross-thread synchronization.
	_, _, errno := syscall.Syscall(
		uintptr(unix.SYS_SECCOMP),
		uintptr(unix.SECCOMP_SET_MODE_FILTER),
		uintptr(unix.SECCOMP_FILTER_FLAG_TSYNC),
		uintptr(unsafe.Pointer(&prog)),
	)
	if errno != 0 {
		return fmt.Errorf("seccomp(SECCOMP_SET_MODE_FILTER, TSYNC): %w", errno)
	}

	return nil
}

// seccomp_data field offsets (from linux/seccomp.h).
const (
	offsetNr   = 0  // uint32: syscall number
	offsetArch = 4  // uint32: audit architecture
	offsetArgs = 16 // uint64[6]: syscall arguments (BPF loads 32-bit words)
)

// buildSeccompFilter constructs the BPF filter program. The filter:
//  1. Validates the architecture (rejects x32/compat ABI bypasses)
//  2. Inspects clone(2) flags for CLONE_NEWUSER/CLONE_NEWNET
//  3. Blocks dangerous syscalls unconditionally
//  4. Allows everything else
//
// CLONE_NEWPID/UTS/IPC/CGROUP/TIME are not blocked — they cannot escape the
// network perimeter or grant new capabilities, and blocking them would prevent
// normal process management.
func buildSeccompFilter() []unix.SockFilter {
	// Architecture-specific constants selected at compile time.
	auditArch := nativeAuditArch()
	sysClone := uint32(unix.SYS_CLONE)
	sysClone3 := uint32(unix.SYS_CLONE3)
	sysUnshare := uint32(unix.SYS_UNSHARE)
	sysSetns := uint32(unix.SYS_SETNS)
	sysMount := uint32(unix.SYS_MOUNT)
	sysUmount2 := uint32(unix.SYS_UMOUNT2)
	sysPtrace := uint32(unix.SYS_PTRACE)
	sysChroot := uint32(unix.SYS_CHROOT)
	sysPivotRoot := uint32(unix.SYS_PIVOT_ROOT)
	sysKexecLoad := uint32(unix.SYS_KEXEC_LOAD)

	ret := func(val uint32) unix.SockFilter {
		return unix.SockFilter{Code: bpfRet | bpfK, K: val}
	}
	deny := ret(unix.SECCOMP_RET_ERRNO | (uint32(syscall.EPERM) & unix.SECCOMP_RET_DATA))
	allow := ret(unix.SECCOMP_RET_ALLOW)

	return []unix.SockFilter{
		// [0] Load architecture.
		{Code: bpfLdW, K: offsetArch},
		// [1] If arch != native → kill (reject unknown ABIs).
		bpfJeq(auditArch, 1, 0),
		ret(unix.SECCOMP_RET_KILL),

		// [3] Load syscall number.
		{Code: bpfLdW, K: offsetNr},

		// [4] clone → jump to flag inspection at [24].
		bpfJeq(sysClone, 19, 0),

		// [5] Block clone3 unconditionally.
		bpfJeq(sysClone3, 0, 1),
		deny,

		// [7] Block unshare.
		bpfJeq(sysUnshare, 0, 1),
		deny,

		// [9] Block setns.
		bpfJeq(sysSetns, 0, 1),
		deny,

		// [11] Block mount.
		bpfJeq(sysMount, 0, 1),
		deny,

		// [13] Block umount2.
		bpfJeq(sysUmount2, 0, 1),
		deny,

		// [15] Block ptrace — prevents inspecting/injecting other processes.
		bpfJeq(sysPtrace, 0, 1),
		deny,

		// [17] Block chroot — prevents filesystem root manipulation.
		bpfJeq(sysChroot, 0, 1),
		deny,

		// [19] Block pivot_root — prevents filesystem root manipulation.
		bpfJeq(sysPivotRoot, 0, 1),
		deny,

		// [21] Block kexec_load — prevents loading a new kernel.
		bpfJeq(sysKexecLoad, 0, 1),
		deny,

		// [23] Allow (syscall didn't match any blocked one, and not clone).
		allow,

		// --- clone(2) flag inspection ---
		// [24] Load clone flags (arg[0], low 32 bits).
		{Code: bpfLdW, K: offsetArgs},

		// [25] Mask with CLONE_NEWUSER, check if set.
		{Code: bpfAluAnd | bpfK, K: syscall.CLONE_NEWUSER},
		bpfJeq(syscall.CLONE_NEWUSER, 0, 1),
		deny,

		// [28] Reload flags for CLONE_NEWNET check.
		{Code: bpfLdW, K: offsetArgs},
		{Code: bpfAluAnd | bpfK, K: syscall.CLONE_NEWNET},
		bpfJeq(syscall.CLONE_NEWNET, 0, 1),
		deny,

		// [32] clone without dangerous flags → allow (normal fork/threads).
		allow,
	}
}

// BPF instruction constants (from linux/filter.h).
const (
	bpfLdW    = 0x20         // BPF_LD | BPF_W | BPF_ABS — load 32-bit word at absolute offset
	bpfRet    = 0x06         // BPF_RET
	bpfK      = 0x00         // BPF_K (constant operand)
	bpfJmp    = 0x05         // BPF_JMP
	bpfJeqK   = 0x10        // BPF_JEQ (with BPF_K)
	bpfAluAnd = 0x04 | 0x50 // BPF_ALU | BPF_AND
)

// bpfJeq returns a BPF instruction that jumps jt instructions forward if the
// accumulator equals val, or jf instructions forward otherwise.
func bpfJeq(val uint32, jt, jf uint8) unix.SockFilter {
	return unix.SockFilter{Code: bpfJmp | bpfJeqK, Jt: jt, Jf: jf, K: val}
}

// nativeAuditArch returns the AUDIT_ARCH constant for the current platform.
func nativeAuditArch() uint32 {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64
	default:
		panic(fmt.Sprintf("unsupported architecture for seccomp filter: %s", runtime.GOARCH))
	}
}

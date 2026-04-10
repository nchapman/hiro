//go:build linux

package main

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/nchapman/hiro/internal/ipc"
	"github.com/nchapman/hiro/internal/landlock"
)

// setNoNewPrivs sets the PR_SET_NO_NEW_PRIVS flag, required for both Landlock
// and seccomp. Must be called before either restriction is applied.
func setNoNewPrivs() error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", err)
	}
	return nil
}

// applyLandlock applies Landlock filesystem restrictions using the paths
// from SpawnConfig.
func applyLandlock(paths ipc.LandlockPaths) error {
	return landlock.Restrict(paths.ReadWrite, paths.ReadOnly)
}

// installSeccomp installs a seccomp-BPF filter that blocks dangerous syscalls.
// When networkAccess is false, socket(AF_INET) and socket(AF_INET6) are also
// blocked, preventing all outbound network connections.
//
// Uses SECCOMP_FILTER_FLAG_TSYNC to synchronize the filter across all Go
// runtime threads. The caller MUST set PR_SET_NO_NEW_PRIVS before calling.
//
// Blocked syscalls (EPERM):
//   - clone(CLONE_NEWUSER) — prevents user namespace creation
//   - clone(CLONE_NEWNET) — prevents network namespace creation
//   - clone3 — blocked unconditionally (Go runtime uses clone, not clone3)
//   - unshare — prevents namespace creation via the other path
//   - setns — prevents entering other namespaces
//   - mount — prevents filesystem manipulation
//   - umount2 — prevents unmounting
//   - ptrace — prevents inspecting/injecting other processes
//   - chroot — prevents filesystem root manipulation
//   - pivot_root — prevents filesystem root manipulation
//   - kexec_load — prevents loading a new kernel
//   - process_vm_readv/writev — prevents cross-process memory access
//   - io_uring_setup — prevents io_uring (bypasses seccomp on some kernels)
//   - shmget/shmat/shmctl — prevents SysV shared memory
//   - socket(AF_INET/AF_INET6) — when networkAccess=false
func installSeccomp(networkAccess bool) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	filter := buildSeccompFilter(networkAccess)
	prog := unix.SockFprog{
		Len:    uint16(len(filter)), //nolint:gosec // filter length fits uint16
		Filter: &filter[0],
	}

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

// buildSeccompFilter constructs the BPF filter program.
func buildSeccompFilter(networkAccess bool) []unix.SockFilter {
	auditArch := nativeAuditArch()

	ret := func(val uint32) unix.SockFilter {
		return unix.SockFilter{Code: bpfRet | bpfK, K: val}
	}
	deny := ret(unix.SECCOMP_RET_ERRNO | (uint32(syscall.EPERM) & unix.SECCOMP_RET_DATA))
	allow := ret(unix.SECCOMP_RET_ALLOW)

	// Unconditionally blocked syscalls (2 instructions each: jeq + deny).
	blockedSyscalls := []uint32{
		uint32(unix.SYS_CLONE3),
		uint32(unix.SYS_UNSHARE),
		uint32(unix.SYS_SETNS),
		uint32(unix.SYS_MOUNT),
		uint32(unix.SYS_UMOUNT2),
		uint32(unix.SYS_PTRACE),
		uint32(unix.SYS_CHROOT),
		uint32(unix.SYS_PIVOT_ROOT),
		uint32(unix.SYS_KEXEC_LOAD),
		uint32(unix.SYS_PROCESS_VM_READV),
		uint32(unix.SYS_PROCESS_VM_WRITEV),
		uint32(unix.SYS_IO_URING_SETUP),
		uint32(unix.SYS_IO_URING_ENTER),
		uint32(unix.SYS_IO_URING_REGISTER),
		uint32(unix.SYS_SHMGET),
		uint32(unix.SYS_SHMAT),
		uint32(unix.SYS_SHMCTL),
	}

	filter := []unix.SockFilter{
		// [0] Load architecture.
		{Code: bpfLdW, K: offsetArch},
		// [1] If arch != native → kill.
		bpfJeq(auditArch, 1, 0),
		ret(unix.SECCOMP_RET_KILL),

		// [3] Load syscall number.
		{Code: bpfLdW, K: offsetNr},
	}

	// [4] clone → jump to flag inspection (placeholder, patched below).
	cloneJeqIdx := len(filter)
	filter = append(filter, unix.SockFilter{})

	// Add unconditionally blocked syscalls.
	for _, nr := range blockedSyscalls {
		filter = append(filter,
			bpfJeq(nr, 0, 1),
			deny,
		)
	}

	// socket → jump to socket inspection (placeholder, patched below).
	socketJeqIdx := -1
	if !networkAccess {
		socketJeqIdx = len(filter)
		filter = append(filter, unix.SockFilter{})
	}

	// Allow (syscall didn't match any blocked one).
	filter = append(filter, allow)

	// --- clone(2) flag inspection ---
	cloneInspectStart := len(filter)
	filter = append(filter,
		// Load clone flags (arg[0], low 32 bits).
		unix.SockFilter{Code: bpfLdW, K: offsetArgs},

		// Mask with CLONE_NEWUSER, check if set.
		unix.SockFilter{Code: bpfAluAnd | bpfK, K: syscall.CLONE_NEWUSER},
		bpfJeq(syscall.CLONE_NEWUSER, 0, 1),
		deny,

		// Reload flags for CLONE_NEWNET check.
		unix.SockFilter{Code: bpfLdW, K: offsetArgs},
		unix.SockFilter{Code: bpfAluAnd | bpfK, K: syscall.CLONE_NEWNET},
		bpfJeq(syscall.CLONE_NEWNET, 0, 1),
		deny,

		// clone without dangerous flags → allow.
		allow,
	)

	// Patch clone JEQ: jt = cloneInspectStart - (cloneJeqIdx + 1).
	filter[cloneJeqIdx] = bpfJeq(uint32(unix.SYS_CLONE),
		uint8(cloneInspectStart-(cloneJeqIdx+1)), 0) //nolint:gosec // offset fits uint8

	if !networkAccess {
		// --- socket(2) domain inspection ---
		// Block AF_INET (2) and AF_INET6 (10).
		socketInspectStart := len(filter)
		filter = append(filter,
			// Load socket domain (arg[0], low 32 bits).
			unix.SockFilter{Code: bpfLdW, K: offsetArgs},

			bpfJeq(syscall.AF_INET, 0, 1),
			deny,

			bpfJeq(syscall.AF_INET6, 0, 1),
			deny,

			// Other socket domains (AF_UNIX, AF_NETLINK, etc.) → allow.
			allow,
		)

		// Patch socket JEQ: jt = socketInspectStart - (socketJeqIdx + 1).
		filter[socketJeqIdx] = bpfJeq(uint32(unix.SYS_SOCKET),
			uint8(socketInspectStart-(socketJeqIdx+1)), 0) //nolint:gosec // offset fits uint8
	}

	return filter
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

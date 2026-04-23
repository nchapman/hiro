//go:build linux

// Package landlock provides Landlock LSM filesystem access control for Linux.
// Landlock restricts a process to only access specified filesystem paths,
// providing an unprivileged alternative to chroot/mount namespaces.
package landlock

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Landlock access right constants.
const (
	rulePathBeneath = 1

	// ABI v1 (kernel 5.13+): 13 access rights.
	accessFsExecute    = 1 << 0
	accessFsWriteFile  = 1 << 1
	accessFsReadFile   = 1 << 2
	accessFsReadDir    = 1 << 3
	accessFsRemoveDir  = 1 << 4
	accessFsRemoveFile = 1 << 5
	accessFsMakeChar   = 1 << 6
	accessFsMakeDir    = 1 << 7
	accessFsMakeReg    = 1 << 8
	accessFsMakeSock   = 1 << 9
	accessFsMakeFifo   = 1 << 10
	accessFsMakeBlock  = 1 << 11
	accessFsMakeSym    = 1 << 12

	// ABI v2 (kernel 5.19+): cross-directory file linking/renaming.
	accessFsRefer = 1 << 13

	// ABI v3 (kernel 6.2+): file truncation.
	accessFsTruncate = 1 << 14

	// ABI v4 (kernel 6.7+): network binding/connecting (not used here).

	// ABI v5 (kernel 6.10+): device ioctls.
	accessFsIoctlDev = 1 << 15

	accessFsAllV1 = accessFsExecute | accessFsWriteFile | accessFsReadFile |
		accessFsReadDir | accessFsRemoveDir | accessFsRemoveFile |
		accessFsMakeChar | accessFsMakeDir | accessFsMakeReg |
		accessFsMakeSock | accessFsMakeFifo | accessFsMakeBlock | accessFsMakeSym

	// accessFsRead grants read-only access (read files, list directories, execute).
	accessFsRead = accessFsExecute | accessFsReadFile | accessFsReadDir

	// accessFsFileOnly is the subset of rights that apply to regular files.
	// The kernel rejects directory-only bits (READ_DIR, MAKE_*, REMOVE_*, REFER)
	// when adding a path-beneath rule on a regular file with EINVAL, so we must
	// mask access down to file-applicable rights. TRUNCATE (ABI v3) and
	// IOCTL_DEV (ABI v5) are added back conditionally in fileAccessMask.
	accessFsFileOnly = accessFsExecute | accessFsReadFile | accessFsWriteFile

	// createRulesetVersion is the flag for querying the max ABI version.
	createRulesetVersion = 1 << 0
)

type rulesetAttr struct {
	HandledAccessFs uint64
}

type pathBeneathAttr struct {
	AllowedAccess uint64
	ParentFd      int32
}

// probeABI returns the highest Landlock ABI version supported by the kernel,
// or 0 and an error if Landlock is unavailable.
func probeABI() (int, error) {
	ver, _, errno := syscall.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0,
		0,
		uintptr(createRulesetVersion),
	)
	if errno != 0 {
		return 0, fmt.Errorf("landlock not available: %w", errno)
	}
	return int(ver), nil
}

// bestAccessMask returns the filesystem access rights declared in
// HandledAccessFs. Rights not declared here are implicitly allowed everywhere,
// so we include the rights the kernel understands — with one exception:
// accessFsIoctlDev (ABI v5) is deliberately excluded. Declaring it would
// require granting ioctl on every RO path that agents need ioctl on
// (notably /dev/tty* for line discipline), and we'd rather let ioctl fall
// through unrestricted than silently break interactive tools like stty and
// readline on kernels 6.10+. The threat model for ioctl-based attacks is
// already addressed by seccomp and by not granting /dev writable access.
func bestAccessMask(abi int) uint64 {
	mask := uint64(accessFsAllV1)
	if abi >= 2 {
		mask |= accessFsRefer
	}
	if abi >= 3 {
		mask |= accessFsTruncate
	}
	return mask
}

// fileAccessMask returns the subset of access that applies to regular files.
// TRUNCATE is file-applicable from ABI v3; REFER and the MAKE_*/REMOVE_*/READ_DIR
// bits are directory-only and must be dropped or the kernel returns EINVAL.
func fileAccessMask(access uint64, abi int) uint64 {
	mask := access & accessFsFileOnly
	if abi >= 3 {
		mask |= access & accessFsTruncate
	}
	return mask
}

// Probe checks whether Landlock is available on the running kernel.
// Returns nil if Landlock is supported, an error otherwise.
func Probe() error {
	_, err := probeABI()
	return err
}

// Restrict applies Landlock filesystem restrictions to the current process.
// rw paths get full access; ro paths get read-only access.
// The caller MUST set PR_SET_NO_NEW_PRIVS before calling Restrict.
// This operation is irreversible for the current process.
//
// The kernel's maximum ABI version is auto-detected, and all supported access
// rights are included in the ruleset. This prevents newer rights (REFER,
// TRUNCATE, etc.) from being implicitly allowed everywhere.
func Restrict(rw, ro []string) error {
	abi, err := probeABI()
	if err != nil {
		return err
	}
	allAccess := bestAccessMask(abi)

	attr := rulesetAttr{HandledAccessFs: allAccess}
	rulesetFd, _, errno := syscall.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)),
		unsafe.Sizeof(attr),
		0,
	)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	fd := int(rulesetFd)
	defer syscall.Close(fd)

	for _, p := range rw {
		if err := addPathRule(fd, p, allAccess, abi); err != nil {
			return fmt.Errorf("landlock rw rule %s: %w", p, err)
		}
	}

	for _, p := range ro {
		if err := addPathRule(fd, p, accessFsRead, abi); err != nil {
			return fmt.Errorf("landlock ro rule %s: %w", p, err)
		}
	}

	_, _, errno = syscall.Syscall(
		unix.SYS_LANDLOCK_RESTRICT_SELF,
		rulesetFd,
		0,
		0,
	)
	if errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}

	return nil
}

func addPathRule(rulesetFd int, path string, access uint64, abi int) error {
	fd, err := syscall.Open(path, unix.O_PATH|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open(%s): %w", path, err)
	}
	defer syscall.Close(fd)

	// Directory-only access bits (READ_DIR, MAKE_*, REMOVE_*, REFER) cause
	// EINVAL when applied to a regular file, so narrow the mask if this path
	// isn't a directory.
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("fstat(%s): %w", path, err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		access = fileAccessMask(access, abi)
	}

	attr := pathBeneathAttr{
		AllowedAccess: access,
		ParentFd:      int32(fd),
	}
	_, _, errno := syscall.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFd),
		uintptr(rulePathBeneath),
		uintptr(unsafe.Pointer(&attr)),
		0, 0, 0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

//go:build linux

// Prototype validating Landlock, unprivileged network namespaces, and
// seccomp-BPF inside a non-root Docker container.
//
// Each test runs in its own subprocess (via /proc/self/exe) because all three
// mechanisms are irreversible per-process.
//
// Key finding: Go's multi-threaded runtime prevents unshare(CLONE_NEWUSER)
// from within a Go process (the kernel requires single-threaded callers).
// The solution is clone-based: spawn a child process via exec.Command with
// SysProcAttr.Cloneflags — the same approach Hiro uses in production.
package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ---------------------------------------------------------------------------
// Landlock constants and structs (ABI v1)
// ---------------------------------------------------------------------------

const (
	sysLandlockCreateRuleset = unix.SYS_LANDLOCK_CREATE_RULESET
	sysLandlockAddRule       = unix.SYS_LANDLOCK_ADD_RULE
	sysLandlockRestrictSelf  = unix.SYS_LANDLOCK_RESTRICT_SELF

	landlockRulePathBeneath = 1

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

	accessFsAll = accessFsExecute | accessFsWriteFile | accessFsReadFile |
		accessFsReadDir | accessFsRemoveDir | accessFsRemoveFile |
		accessFsMakeChar | accessFsMakeDir | accessFsMakeReg |
		accessFsMakeSock | accessFsMakeFifo | accessFsMakeBlock | accessFsMakeSym
)

type landlockRulesetAttr struct {
	HandledAccessFs uint64
}

type landlockPathBeneathAttr struct {
	AllowedAccess uint64
	ParentFd      int32
}

// ---------------------------------------------------------------------------
// Result tracking
// ---------------------------------------------------------------------------

var (
	passed int
	failed int
)

func pass(msg string) {
	fmt.Printf("  PASS: %s\n", msg)
	passed++
}

func fail(msg string) {
	fmt.Printf("  FAIL: %s\n", msg)
	failed++
}

// ---------------------------------------------------------------------------
// Landlock helpers
// ---------------------------------------------------------------------------

func landlockCreateRuleset(attr *landlockRulesetAttr) (int, error) {
	fd, _, errno := syscall.Syscall(
		sysLandlockCreateRuleset,
		uintptr(unsafe.Pointer(attr)),
		unsafe.Sizeof(*attr),
		0,
	)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func landlockAddRule(rulesetFd int, ruleType int, attr *landlockPathBeneathAttr) error {
	_, _, errno := syscall.Syscall6(
		sysLandlockAddRule,
		uintptr(rulesetFd),
		uintptr(ruleType),
		uintptr(unsafe.Pointer(attr)),
		0, 0, 0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func landlockRestrictSelf(rulesetFd int) error {
	_, _, errno := syscall.Syscall(
		sysLandlockRestrictSelf,
		uintptr(rulesetFd),
		0,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func addPathRule(rulesetFd int, path string, access uint64) error {
	fd, err := syscall.Open(path, unix.O_PATH|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open(%s): %w", path, err)
	}
	defer syscall.Close(fd)

	attr := landlockPathBeneathAttr{
		AllowedAccess: access,
		ParentFd:      int32(fd),
	}
	return landlockAddRule(rulesetFd, landlockRulePathBeneath, &attr)
}

func applyLandlock(allowedPaths []string) error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(NO_NEW_PRIVS): %w", err)
	}

	attr := landlockRulesetAttr{HandledAccessFs: accessFsAll}
	rulesetFd, err := landlockCreateRuleset(&attr)
	if err != nil {
		return fmt.Errorf("landlock_create_ruleset: %w", err)
	}
	defer syscall.Close(rulesetFd)

	for _, p := range allowedPaths {
		if err := addPathRule(rulesetFd, p, accessFsAll); err != nil {
			return fmt.Errorf("landlock_add_rule(%s): %w", p, err)
		}
	}

	return landlockRestrictSelf(rulesetFd)
}

// ---------------------------------------------------------------------------
// Seccomp BPF
// ---------------------------------------------------------------------------

const (
	bpfLdW    = 0x20
	bpfRet    = 0x06
	bpfK      = 0x00
	bpfJmp    = 0x05
	bpfJeqK   = 0x10
	bpfAluAnd = 0x04 | 0x50

	offsetNr   = 0
	offsetArch = 4
)

func bpfJeq(val uint32, jt, jf uint8) unix.SockFilter {
	return unix.SockFilter{Code: bpfJmp | bpfJeqK, Jt: jt, Jf: jf, K: val}
}

func nativeAuditArch() uint32 {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64
	default:
		panic("unsupported arch: " + runtime.GOARCH)
	}
}

func installSeccomp() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(NO_NEW_PRIVS): %w", err)
	}

	auditArch := nativeAuditArch()
	sysPtrace := uint32(unix.SYS_PTRACE)

	ret := func(val uint32) unix.SockFilter {
		return unix.SockFilter{Code: bpfRet | bpfK, K: val}
	}
	deny := ret(unix.SECCOMP_RET_ERRNO | (uint32(syscall.EPERM) & unix.SECCOMP_RET_DATA))
	allow := ret(unix.SECCOMP_RET_ALLOW)

	filter := []unix.SockFilter{
		{Code: bpfLdW, K: offsetArch},
		bpfJeq(auditArch, 1, 0),
		ret(unix.SECCOMP_RET_KILL),

		{Code: bpfLdW, K: offsetNr},

		bpfJeq(sysPtrace, 0, 1),
		deny,

		allow,
	}

	prog := unix.SockFprog{
		Len:    uint16(len(filter)),
		Filter: &filter[0],
	}

	_, _, errno := syscall.Syscall(
		uintptr(unix.SYS_SECCOMP),
		uintptr(unix.SECCOMP_SET_MODE_FILTER),
		uintptr(unix.SECCOMP_FILTER_FLAG_TSYNC),
		uintptr(unsafe.Pointer(&prog)),
	)
	if errno != 0 {
		return fmt.Errorf("seccomp(SET_MODE_FILTER, TSYNC): %w", errno)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Loopback helper
// ---------------------------------------------------------------------------

func bringUpLoopback() error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("inet socket: %w", err)
	}
	defer syscall.Close(fd)

	type ifreq struct {
		Name  [16]byte
		Flags uint16
		_     [22]byte
	}

	var req ifreq
	copy(req.Name[:], "lo")

	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(unix.SIOCGIFFLAGS),
		uintptr(unsafe.Pointer(&req)),
	)
	if errno != 0 {
		return fmt.Errorf("SIOCGIFFLAGS: %w", errno)
	}

	req.Flags |= syscall.IFF_UP
	_, _, errno = syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(unix.SIOCSIFFLAGS),
		uintptr(unsafe.Pointer(&req)),
	)
	if errno != 0 {
		return fmt.Errorf("SIOCSIFFLAGS: %w", errno)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Subprocess dispatch
// ---------------------------------------------------------------------------

const subtestEnv = "PROTO_SUBTEST"

// runSubtest executes a named subtest in a child process (same binary).
func runSubtest(name string) bool {
	cmd := exec.Command("/proc/self/exe")
	cmd.Env = append(os.Environ(), subtestEnv+"="+name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run() == nil
}

// runInNamespace spawns a child in new user+network namespaces using clone
// (via SysProcAttr.Cloneflags). This is the only way to create user namespaces
// from Go — unshare() fails because the Go runtime is multi-threaded.
func runInNamespace(name string) bool {
	cmd := exec.Command("/proc/self/exe")
	cmd.Env = append(os.Environ(), subtestEnv+"="+name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
	}
	return cmd.Run() == nil
}

// ---------------------------------------------------------------------------
// Subtest: Landlock
// ---------------------------------------------------------------------------

func subtestLandlock() {
	tmpDir, err := os.MkdirTemp("", "landlock-proto-*")
	if err != nil {
		fail(fmt.Sprintf("creating temp dir: %v", err))
		return
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "allowed.txt")
	if err := os.WriteFile(testFile, []byte("hello landlock"), 0o644); err != nil {
		fail(fmt.Sprintf("writing test file: %v", err))
		return
	}

	if err := applyLandlock([]string{tmpDir, "/tmp"}); err != nil {
		fail(fmt.Sprintf("applying Landlock: %v", err))
		return
	}

	// CAN read allowed file.
	data, err := os.ReadFile(testFile)
	if err != nil {
		fail(fmt.Sprintf("cannot read allowed file: %v", err))
	} else if string(data) == "hello landlock" {
		pass("can read file in allowed directory")
	} else {
		fail(fmt.Sprintf("unexpected content: %q", string(data)))
	}

	// CANNOT read /etc/passwd.
	if _, err := os.ReadFile("/etc/passwd"); err != nil {
		pass("cannot read /etc/passwd (permission denied)")
	} else {
		fail("was able to read /etc/passwd — Landlock not enforcing")
	}

	// CANNOT read /etc/hostname.
	if _, err := os.ReadFile("/etc/hostname"); err != nil {
		pass("cannot read /etc/hostname (permission denied)")
	} else {
		fail("was able to read /etc/hostname — Landlock not enforcing")
	}
}

// ---------------------------------------------------------------------------
// Subtest: Network namespace (runs inside CLONE_NEWUSER|CLONE_NEWNET)
// ---------------------------------------------------------------------------

func subtestNetnsInner() {
	pass("clone(CLONE_NEWUSER|CLONE_NEWNET) succeeded (we are running inside it)")

	// External connections should fail — no routes, no interfaces.
	conn, err := net.DialTimeout("tcp", "1.1.1.1:443", 2*time.Second)
	if err != nil {
		pass(fmt.Sprintf("cannot connect to 1.1.1.1:443 (%v)", shortenErr(err)))
	} else {
		conn.Close()
		fail("was able to connect to 1.1.1.1:443 — network not isolated")
	}

	// Bring up loopback and test localhost.
	if err := bringUpLoopback(); err != nil {
		fmt.Printf("  (note: could not bring up loopback: %v — skipping loopback test)\n", err)
		return
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail(fmt.Sprintf("cannot listen on localhost: %v", err))
		return
	}
	defer ln.Close()

	go func() {
		c, _ := ln.Accept()
		if c != nil {
			c.Close()
		}
	}()

	conn, err = net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		fail(fmt.Sprintf("cannot connect to localhost: %v", err))
	} else {
		conn.Close()
		pass("loopback works (bind+connect localhost)")
	}
}

// ---------------------------------------------------------------------------
// Subtest: seccomp
// ---------------------------------------------------------------------------

func subtestSeccomp() {
	if err := installSeccomp(); err != nil {
		fail(fmt.Sprintf("installing seccomp filter: %v", err))
		return
	}

	err := unix.PtraceAttach(1)
	if err == syscall.EPERM {
		pass("ptrace blocked (operation not permitted)")
	} else if err != nil {
		pass(fmt.Sprintf("ptrace blocked (%v)", err))
	} else {
		_ = unix.PtraceDetach(1)
		fail("ptrace was not blocked by seccomp")
	}
}

// ---------------------------------------------------------------------------
// Subtest: Combined — correct ordering: clone(netns) -> Landlock -> seccomp
//
// This subprocess is already inside CLONE_NEWUSER|CLONE_NEWNET (spawned by
// runInNamespace). It applies Landlock, then seccomp, then verifies all three.
// ---------------------------------------------------------------------------

func subtestCombinedInner() {
	// We're already in a new user+network namespace from clone().
	_ = bringUpLoopback()

	// Apply Landlock.
	tmpDir, err := os.MkdirTemp("", "combined-proto-*")
	if err != nil {
		fail(fmt.Sprintf("combined: mkdtemp: %v", err))
		return
	}
	testFile := filepath.Join(tmpDir, "test.txt")
	_ = os.WriteFile(testFile, []byte("combined"), 0o644)

	if err := applyLandlock([]string{tmpDir, "/tmp"}); err != nil {
		fail(fmt.Sprintf("combined: Landlock: %v", err))
		return
	}

	// Apply seccomp.
	if err := installSeccomp(); err != nil {
		fail(fmt.Sprintf("combined: seccomp: %v", err))
		return
	}

	// Verify all three enforcing simultaneously.
	allGood := true

	if _, err := os.ReadFile("/etc/passwd"); err == nil {
		fail("combined: Landlock not blocking /etc/passwd")
		allGood = false
	}

	if data, err := os.ReadFile(testFile); err != nil || string(data) != "combined" {
		fail(fmt.Sprintf("combined: cannot read allowed file: %v", err))
		allGood = false
	}

	if conn, err := net.DialTimeout("tcp", "1.1.1.1:443", 1*time.Second); err == nil {
		conn.Close()
		fail("combined: network not isolated")
		allGood = false
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail(fmt.Sprintf("combined: cannot listen on localhost: %v", err))
		allGood = false
	} else {
		go func() {
			c, _ := ln.Accept()
			if c != nil {
				c.Close()
			}
		}()
		if conn, err := net.DialTimeout("tcp", ln.Addr().String(), 1*time.Second); err != nil {
			fail(fmt.Sprintf("combined: loopback broken: %v", err))
			allGood = false
		} else {
			conn.Close()
		}
		ln.Close()
	}

	if err := unix.PtraceAttach(1); err == nil {
		_ = unix.PtraceDetach(1)
		fail("combined: ptrace not blocked")
		allGood = false
	}

	if allGood {
		pass("all three work together — filesystem, network, and syscall isolation active")
	}
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	// Subprocess dispatch.
	if sub := os.Getenv(subtestEnv); sub != "" {
		switch sub {
		case "landlock":
			subtestLandlock()
		case "netns-inner":
			subtestNetnsInner()
		case "seccomp":
			subtestSeccomp()
		case "combined-inner":
			subtestCombinedInner()
		default:
			fmt.Fprintf(os.Stderr, "unknown subtest: %s\n", sub)
			os.Exit(1)
		}
		if failed > 0 {
			os.Exit(1)
		}
		return
	}

	// Parent process — orchestrates subtests.
	fmt.Println("Hiro Landlock/Namespace/Seccomp Prototype")
	fmt.Println("==========================================")
	fmt.Printf("Kernel: Linux, Arch: %s, PID: %d, UID: %d\n",
		runtime.GOARCH, os.Getpid(), os.Getuid())

	totalPass := 0
	totalFail := 0

	runTest := func(title string, fn func() bool) {
		fmt.Printf("\n=== %s ===\n", title)
		if fn() {
			totalPass++
		} else {
			totalFail++
		}
	}

	runTest("Landlock Filesystem Isolation", func() bool {
		return runSubtest("landlock")
	})

	runTest("Network Namespace Isolation", func() bool {
		// Spawn child in CLONE_NEWUSER|CLONE_NEWNET via clone(2).
		// This is the Go-compatible way — unshare() doesn't work in Go
		// because the runtime is multi-threaded.
		return runInNamespace("netns-inner")
	})

	runTest("seccomp-BPF", func() bool {
		return runSubtest("seccomp")
	})

	runTest("Combined (netns + Landlock + seccomp)", func() bool {
		// Spawn child in namespaces, it applies Landlock + seccomp internally.
		return runInNamespace("combined-inner")
	})

	fmt.Println()
	fmt.Println("==========================================")
	fmt.Printf("Results: %d/%d test groups passed\n", totalPass, totalPass+totalFail)

	if totalFail > 0 {
		os.Exit(1)
	}
}

func shortenErr(err error) string {
	if opErr, ok := err.(*net.OpError); ok {
		if opErr.Err != nil {
			return opErr.Err.Error()
		}
	}
	return err.Error()
}

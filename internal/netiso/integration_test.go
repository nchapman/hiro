//go:build netiso

package netiso

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestProbe verifies that CAP_NET_ADMIN is available.
func TestProbe(t *testing.T) {
	if err := Probe(); err != nil {
		t.Fatalf("Probe() failed — CAP_NET_ADMIN not available: %v", err)
	}
}

// TestNewAndClose verifies that NetIso can be created and closed cleanly.
func TestNewAndClose(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ni, err := New(logger)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	if err := ni.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}
}

// TestSetupAndTeardown spawns a real process in a network namespace,
// verifies veth creation and nftables rules, then tears down.
func TestSetupAndTeardown(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ni, err := New(logger)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer ni.Close()

	// Spawn a process in a new network namespace.
	// Use 'sleep' as a long-running process we can inspect.
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNET | syscall.CLONE_NEWNS,
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep process: %v", err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	agent := AgentNetwork{
		AgentID:   0,
		SessionID: "test-sess-001",
		PID:       cmd.Process.Pid,
		Egress:    []string{"github.com", "*.npmjs.org"},
	}

	// Setup network isolation.
	if err := ni.Setup(context.Background(), agent); err != nil {
		t.Fatalf("Setup() failed: %v", err)
	}

	// Verify host-side veth exists.
	hostIF := "hiro-test-sess-"
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("listing interfaces: %v", err)
	}
	found := false
	for _, iface := range ifaces {
		if strings.HasPrefix(iface.Name, "hiro-") {
			found = true
			t.Logf("found host veth: %s", iface.Name)
		}
	}
	if !found {
		t.Errorf("host-side veth starting with %q not found", hostIF)
	}

	// Verify agent-side has eth0 by running 'ip addr' in the namespace.
	nsExec := exec.Command("nsenter", "--net", fmt.Sprintf("--target=%d", cmd.Process.Pid),
		"ip", "addr", "show", "eth0")
	out, err := nsExec.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to check eth0 in agent ns: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "10.0.0.2") {
		t.Errorf("agent eth0 does not have expected IP 10.0.0.2:\n%s", out)
	}

	// Verify agent's resolv.conf points to gateway.
	nsExec = exec.Command("nsenter", "--net", "--mount", fmt.Sprintf("--target=%d", cmd.Process.Pid),
		"cat", "/etc/resolv.conf")
	out, err = nsExec.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read resolv.conf in agent ns: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "10.0.0.1") {
		t.Errorf("agent resolv.conf does not point to gateway 10.0.0.1:\n%s", out)
	}

	// Verify nftables rules exist.
	nftOut, err := exec.Command("nft", "list", "table", "inet", "hiro").CombinedOutput()
	if err != nil {
		t.Fatalf("failed to list nftables: %v\n%s", err, nftOut)
	}
	nftStr := string(nftOut)
	// Session prefix is truncated to 12 chars.
	if !strings.Contains(nftStr, agent.SessionPrefix()) {
		t.Errorf("nftables does not contain agent chain/set (prefix %s):\n%s", agent.SessionPrefix(), nftStr)
	}
	t.Logf("nftables rules:\n%s", nftStr)

	// Teardown.
	if err := ni.Teardown("test-sess-001"); err != nil {
		t.Fatalf("Teardown() failed: %v", err)
	}

	// Verify host-side veth is gone.
	ifaces, _ = net.Interfaces()
	for _, iface := range ifaces {
		if strings.HasPrefix(iface.Name, "hiro-") {
			t.Errorf("host veth %q still exists after teardown", iface.Name)
		}
	}

	// Verify nftables rules are cleaned up.
	nftOut, err = exec.Command("nft", "list", "table", "inet", "hiro").CombinedOutput()
	if err != nil {
		// Table might be gone entirely after close — that's fine.
		t.Logf("nftables table gone after teardown (expected): %v", err)
	} else if strings.Contains(string(nftOut), agent.SessionPrefix()+"_ips") {
		t.Errorf("nftables still contains agent set after teardown:\n%s", nftOut)
	}
}

// TestDNSForwarder verifies that the DNS forwarder resolves allowed domains
// and returns NXDOMAIN for denied domains.
func TestDNSForwarder(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ni, err := New(logger)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer ni.Close()

	// Spawn a process in a new network namespace.
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNET | syscall.CLONE_NEWNS,
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep process: %v", err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	agent := AgentNetwork{
		AgentID:   1,
		SessionID: "test-dns-00001",
		PID:       cmd.Process.Pid,
		Egress:    []string{"example.com"},
	}

	if err := ni.Setup(context.Background(), agent); err != nil {
		t.Fatalf("Setup() failed: %v", err)
	}
	defer ni.Teardown("test-dns-00001")

	// Give DNS server a moment to start listening.
	time.Sleep(200 * time.Millisecond)

	// Test DNS directly from the test process (not via nsenter) by querying
	// the DNS forwarder on the gateway IP. This works because the gateway IP
	// is on the host-side veth, reachable from the host namespace.
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, "udp", "10.0.1.1:53")
		},
	}

	// Allowed domain should resolve.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := resolver.LookupHost(ctx, "example.com")
	if err != nil {
		t.Fatalf("lookup example.com failed: %v", err)
	}
	t.Logf("example.com resolved to: %v", ips)
	if len(ips) == 0 {
		t.Error("example.com resolved to 0 IPs")
	}

	// Denied domain should fail (NXDOMAIN).
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	ips, err = resolver.LookupHost(ctx2, "evil.com")
	if err == nil {
		t.Errorf("lookup evil.com should fail, got IPs: %v", ips)
	} else {
		t.Logf("evil.com correctly denied: %v", err)
	}
}

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

// spawnNamespaced starts a process in its own user, network, and mount namespaces
// using CLONE_NEWUSER (matching the production spawn model). The UID mapping maps
// root inside to the current UID outside.
func spawnNamespaced(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start namespaced process: %v", err)
	}
	return cmd
}

// TestSetupAndTeardown spawns a real process in a user+network namespace,
// verifies veth creation and nftables rules, then tears down.
// Agent-side configuration (eth0, bind mounts) is NOT verified here —
// that's the child's responsibility in the production spawn protocol.
func TestSetupAndTeardown(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ni, err := New(logger)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer ni.Close()

	cmd := spawnNamespaced(t)
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

	// Setup network isolation (host side only).
	if err := ni.Setup(context.Background(), agent); err != nil {
		t.Fatalf("Setup() failed: %v", err)
	}
	peerName := PeerName(agent.SessionPrefix())
	t.Logf("peer name: %s", peerName)

	// Verify host-side veth exists.
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
		t.Error("host-side veth not found")
	}

	// Verify the veth peer was moved into the child's namespace.
	// The peer should NOT be visible in the host namespace.
	_, err = net.InterfaceByName(peerName)
	if err == nil {
		t.Errorf("peer %s should have been moved to child ns, but is still in host ns", peerName)
	}

	// Verify nftables rules exist.
	nftOut, err := exec.Command("nft", "list", "table", "inet", "hiro").CombinedOutput()
	if err != nil {
		t.Fatalf("failed to list nftables: %v\n%s", err, nftOut)
	}
	nftStr := string(nftOut)
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

	cmd := spawnNamespaced(t)
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

	// Test DNS directly from the test process by querying the DNS forwarder
	// on the gateway IP (reachable from host namespace via host-side veth).
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, "udp", fmt.Sprintf("%s:53", agent.GatewayIP()))
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

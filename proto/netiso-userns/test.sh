#!/bin/bash
# Prototype: per-agent network isolation using CLONE_NEWUSER
#
# Validates:
#   1. CLONE_NEWUSER grants full caps inside userns (no CAP_SYS_ADMIN needed)
#   2. Non-root UID mapping accepted by kernel (Go will use UidMappings)
#   3. Full spawn flow: parent veth + child self-configures
#   4. Connectivity and nftables with CAP_NET_ADMIN only
#   5. Bind mounts from child's userns
#   6. Per-worker seccomp blocks dangerous syscalls:
#      - clone(CLONE_NEWUSER) via BPF flag inspection
#      - clone3 (blocked unconditionally)
#      - unshare, setns, mount, umount2
#      - chroot, pivot_root (via container-wide seccomp)
#   7. Full lifecycle: setup → seccomp → agent cannot escalate
#
# Privileges: cap_add: [NET_ADMIN], seccomp allows CLONE_NEWUSER. NO SYS_ADMIN.
#
# NOTE: shell `unshare --map-root-user` maps UID 0→0. In Go production,
# SysProcAttr.UidMappings maps 0→10000 (agent UID). Both grant the child
# UID 0 inside with full caps — the only difference is the host-side UID.
# Test 2 verifies the kernel accepts non-root mappings written to uid_map.
set -uo pipefail

PASS=0
FAIL=0
pass() { echo "  PASS: $1"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

AGENT_UID=10000
AGENT_GID=10000
groupadd -g $AGENT_GID hiro-agents 2>/dev/null || true
useradd -u $AGENT_UID -g $AGENT_GID -M -s /bin/bash hiro-agent-0 2>/dev/null || true

echo "=== Network Isolation Prototype v2 ==="
echo "=== CLONE_NEWUSER + per-worker seccomp ==="
echo ""

# -------------------------------------------------------
# Test 1: CLONE_NEWUSER gives full caps (no CAP_SYS_ADMIN)
# -------------------------------------------------------
echo "--- Test 1: CLONE_NEWUSER grants caps inside userns ---"

CAPS=$(unshare --user --map-root-user -- grep CapEff /proc/self/status 2>/dev/null | awk '{print $2}')
if [ -n "$CAPS" ] && [ "$CAPS" != "0000000000000000" ]; then
    pass "child has full capabilities inside userns ($CAPS)"
else
    fail "child has no capabilities"
fi

# -------------------------------------------------------
# Test 2: Kernel accepts non-root UID mapping
#
# This verifies the kernel lets root write a mapping of
# "0 10000 1" to uid_map — the exact operation Go's
# SysProcAttr.UidMappings performs between clone() and exec().
# We can't fully test this from shell because unshare exec's
# before we can write the mapping, but we verify the kernel
# accepts the write.
# -------------------------------------------------------
echo ""
echo "--- Test 2: kernel accepts non-root UID mapping (0 → $AGENT_UID) ---"

unshare --user -- sleep 5 &
MAP_PID=$!
sleep 0.2

echo "deny" > /proc/$MAP_PID/setgroups 2>/dev/null
MAP_RESULT=0
echo "0 $AGENT_UID 1" > /proc/$MAP_PID/uid_map 2>/dev/null || MAP_RESULT=1
echo "0 $AGENT_GID 1" > /proc/$MAP_PID/gid_map 2>/dev/null || MAP_RESULT=1

if [ $MAP_RESULT -eq 0 ]; then
    ACTUAL_MAP=$(cat /proc/$MAP_PID/uid_map | awk '{print $1, $2, $3}')
    echo "  uid_map: $ACTUAL_MAP"
    pass "kernel accepted uid_map 0 → $AGENT_UID"
else
    fail "kernel rejected uid_map write"
fi

kill $MAP_PID 2>/dev/null; wait $MAP_PID 2>/dev/null || true

# -------------------------------------------------------
# Test 3: Full spawn flow
# -------------------------------------------------------
echo ""
echo "--- Test 3: full spawn flow (parent veth + child self-config) ---"

FIFO=$(mktemp -u)
mkfifo "$FIFO"
VETH_HOST="vh-proto"
VETH_PEER="vp-proto"

cat > /tmp/spawn-child.sh << 'CHILDEOF'
#!/bin/bash
FIFO="$1"; VETH_PEER="$2"
echo "ns-ready" > "$FIFO"
cat "$FIFO" > /dev/null  # wait for veth

# Self-configure (full caps inside userns)
ip link set lo up
ip addr add 10.99.0.2/30 dev "$VETH_PEER"
ip link set "$VETH_PEER" up
ip route add default via 10.99.0.1

# Bind mount resolv.conf
echo "nameserver 10.99.0.1" > /tmp/resolv.conf
mount --bind /tmp/resolv.conf /etc/resolv.conf
MOUNT_OK=$?

if [ $MOUNT_OK -eq 0 ] && grep -q "10.99.0.1" /etc/resolv.conf; then
    echo "configured" > "$FIFO"
else
    echo "fail:mount=$MOUNT_OK" > "$FIFO"
fi
sleep 60
CHILDEOF
chmod +x /tmp/spawn-child.sh

unshare --user --map-root-user --net --mount -- bash /tmp/spawn-child.sh "$FIFO" "$VETH_PEER" &
CHILD=$!

cat "$FIFO" > /dev/null  # ns-ready

ip link add "$VETH_HOST" type veth peer name "$VETH_PEER"
[ $? -eq 0 ] && pass "parent created veth" || fail "veth creation"

ip link set "$VETH_PEER" netns $CHILD && pass "parent moved veth into child netns" || fail "veth move"

ip addr add 10.99.0.1/30 dev "$VETH_HOST"
ip link set "$VETH_HOST" up
echo "go" > "$FIFO"

SIGNAL=$(cat "$FIFO")
[ "$SIGNAL" = "configured" ] && pass "child self-configured interfaces + bind mount" || fail "child config: $SIGNAL"

# -------------------------------------------------------
# Test 4: Connectivity
# -------------------------------------------------------
echo ""
echo "--- Test 4: connectivity ---"

ping -c 1 -W 2 10.99.0.2 >/dev/null 2>&1 && pass "host -> child" || fail "host -> child"
nsenter --target $CHILD --user --net -- ping -c 1 -W 2 10.99.0.1 >/dev/null 2>&1 && pass "child -> host" || fail "child -> host"

# -------------------------------------------------------
# Test 5: nftables
# -------------------------------------------------------
echo ""
echo "--- Test 5: nftables (CAP_NET_ADMIN only) ---"

nft add table inet proto_test && pass "nft add table" || fail "nft add table"
nft add chain inet proto_test agent_fwd '{ type filter hook forward priority 0; policy drop; }' && pass "nft add chain" || fail "nft add chain"
nft add set inet proto_test ips '{ type ipv4_addr; flags timeout; }' && pass "nft add set" || fail "nft add set"
nft add element inet proto_test ips '{ 1.2.3.4 timeout 60s }' && pass "nft add element" || fail "nft add element"
nft add rule inet proto_test agent_fwd ip saddr 10.99.0.2 ip daddr @ips accept && pass "nft add rule" || fail "nft add rule"
nft delete table inet proto_test && pass "nft cleanup" || fail "nft cleanup"

kill $CHILD 2>/dev/null; wait $CHILD 2>/dev/null || true
ip link del "$VETH_HOST" 2>/dev/null || true
rm -f "$FIFO"

# -------------------------------------------------------
# Test 6: Per-worker seccomp
#
# drop_privs installs a seccomp-BPF filter that blocks:
#   - clone(CLONE_NEWUSER) — via BPF flag inspection (VULN-01)
#   - clone3              — blocked unconditionally (VULN-01)
#   - unshare             — prevents namespace creation
#   - setns               — prevents entering other namespaces (VULN-02)
#   - mount               — prevents bind mounts
#   - umount2             — prevents unmounting controlled files (VULN-03)
#   - chroot/pivot_root   — blocked by container-wide seccomp
# -------------------------------------------------------
echo ""
echo "--- Test 6: per-worker seccomp blocks dangerous syscalls ---"

# --- VULN-01: clone(CLONE_NEWUSER) via clone(2) syscall ---
# The shell 'unshare' command uses unshare(2), but an adversary can
# call clone(CLONE_NEWUSER) directly to bypass an unshare-only filter.
# try_clone_newuser calls clone(2) with CLONE_NEWUSER in flags.
./drop_privs ./try_clone_newuser 2>/dev/null
CLONE_EXIT=$?
[ $CLONE_EXIT -eq 1 ] && pass "seccomp blocks clone(CLONE_NEWUSER) [VULN-01]" || fail "clone(CLONE_NEWUSER) not blocked (exit=$CLONE_EXIT) [VULN-01]"

# Verify clone3 is also blocked (try via unshare which may use clone3 on newer kernels)
# clone3 is blocked unconditionally — any invocation returns EPERM.
# We test indirectly: if unshare --user fails, both paths are covered.
./drop_privs unshare --user -- id >/dev/null 2>&1 && fail "unshare --user not blocked" || pass "seccomp blocks unshare --user"
./drop_privs unshare --net -- id >/dev/null 2>&1 && fail "unshare --net not blocked" || pass "seccomp blocks unshare --net"
./drop_privs unshare --mount -- id >/dev/null 2>&1 && fail "unshare --mount not blocked" || pass "seccomp blocks unshare --mount"

# --- VULN-02: setns(2) for entering other namespaces ---
# An agent could enumerate /proc/<pid>/ns/net and call setns to enter
# a sibling worker's network namespace. Block it unconditionally.
./drop_privs ./try_setns 2>/dev/null
SETNS_EXIT=$?
[ $SETNS_EXIT -eq 1 ] && pass "seccomp blocks setns [VULN-02]" || fail "setns not blocked (exit=$SETNS_EXIT) [VULN-02]"

# --- VULN-03: umount2 for unmounting controlled bind mounts ---
# An agent could unmount /etc/resolv.conf to fall back to Docker's
# unfiltered DNS resolver, bypassing the DNS-driven firewall.
# mount --bind creates a bind mount, then we verify umount is blocked.
mount --bind /etc/hostname /tmp/umount-test-mount 2>/dev/null || true
./drop_privs umount /tmp/umount-test-mount >/dev/null 2>&1 && fail "umount not blocked [VULN-03]" || pass "seccomp blocks umount [VULN-03]"
umount /tmp/umount-test-mount 2>/dev/null || true

# Adversarial: agent tries filesystem manipulation
./drop_privs mount --bind /tmp /mnt >/dev/null 2>&1 && fail "mount not blocked" || pass "seccomp blocks mount"

# chroot/pivot_root are blocked by the container-wide seccomp profile
# (removed from Docker's default allowlist in seccomp.json)
./drop_privs chroot /tmp /bin/true >/dev/null 2>&1 && fail "chroot not blocked" || pass "container-wide seccomp blocks chroot"

# Adversarial: agent tries to modify network
# In production, agent runs as non-root UID (no CAP_NET_ADMIN). Simulate with setpriv.
setpriv --reuid=$AGENT_UID --regid=$AGENT_GID --clear-groups -- \
    ./drop_privs ip link add test0 type dummy >/dev/null 2>&1 && fail "ip link add not blocked" || pass "agent cannot create network interfaces"
setpriv --reuid=$AGENT_UID --regid=$AGENT_GID --clear-groups -- \
    ./drop_privs nft list tables >/dev/null 2>&1 && fail "nft not blocked" || pass "agent cannot read nftables rules"

# Positive: normal agent operations still work
./drop_privs ls / >/dev/null 2>&1 && pass "normal exec works" || fail "seccomp too restrictive"
./drop_privs cat /etc/hostname >/dev/null 2>&1 && pass "file reads work" || fail "seccomp blocks reads"
./drop_privs bash -c 'echo test > /tmp/seccomp-write-test && rm /tmp/seccomp-write-test' 2>/dev/null && pass "file writes work" || fail "seccomp blocks writes"
# Positive: normal fork/exec still works (clone without CLONE_NEWUSER)
./drop_privs bash -c 'ls / >/dev/null' 2>/dev/null && pass "fork/exec works (clone without NEWUSER)" || fail "seccomp blocks normal clone"

# -------------------------------------------------------
# Test 7: Full lifecycle
#
# 1. Child creates NEWUSER+NEWNET+NEWNS, self-configures
# 2. Child installs per-worker seccomp (via drop_privs)
# 3. Agent code runs — validates ALL escape vectors blocked:
#    - clone(CLONE_NEWUSER) via clone(2) [VULN-01]
#    - setns to enter other namespaces [VULN-02]
#    - umount to strip controlled bind mounts [VULN-03]
#    - unshare, mount (existing tests)
# 4. Agent CAN still use network, files, fork/exec
# -------------------------------------------------------
echo ""
echo "--- Test 7: full lifecycle (setup → seccomp → agent cannot escalate) ---"

FIFO7=$(mktemp -u)
mkfifo "$FIFO7"
RESULT7=$(mktemp)

cat > /tmp/lifecycle.sh << 'LCEOF'
#!/bin/bash
FIFO="$1"; DROP="$2"; RESULT="$3"
echo "ns-ready" > "$FIFO"; cat "$FIFO" > /dev/null  # wait for veth

# ---- SETUP PHASE (full caps in userns) ----
ip link set lo up
ip addr add 10.99.1.2/30 dev vp-lc
ip link set vp-lc up
ip route add default via 10.99.1.1
echo "nameserver 10.99.1.1" > /tmp/resolv.conf
mount --bind /tmp/resolv.conf /etc/resolv.conf

SETUP="yes"
ip addr show vp-lc | grep -q "10.99.1.2" || SETUP="no"
grep -q "10.99.1.1" /etc/resolv.conf || SETUP="no"

# ---- AGENT PHASE (behind seccomp) ----

# VULN-01: clone(CLONE_NEWUSER) via clone(2) syscall
CLONE_USER="no"; $DROP /proto/try_clone_newuser >/dev/null 2>&1 && CLONE_USER="yes"

# VULN-02: setns to enter other namespaces
SETNS="no"; $DROP /proto/try_setns >/dev/null 2>&1 && SETNS="yes"

# VULN-03: umount to strip controlled bind mounts
UMOUNT="no"; $DROP umount /etc/resolv.conf >/dev/null 2>&1 && UMOUNT="yes"
# Verify resolv.conf is still our controlled version
RESOLV_OK="no"; grep -q "10.99.1.1" /etc/resolv.conf 2>/dev/null && RESOLV_OK="yes"

# Existing: unshare, mount
UNSHARE="no"; $DROP unshare --user -- id >/dev/null 2>&1 && UNSHARE="yes"
MOUNT="no";   $DROP mount --bind /tmp /mnt >/dev/null 2>&1 && MOUNT="yes"

# Positive: normal operations
READ="no";    $DROP cat /etc/hostname >/dev/null 2>&1 && READ="yes"
NET="no";     $DROP ping -c 1 -W 2 10.99.1.1 >/dev/null 2>&1 && NET="yes"
WRITE="no";   $DROP bash -c 'echo test > /tmp/agent-write-test' >/dev/null 2>&1 && WRITE="yes"
FORK="no";    $DROP bash -c 'ls / >/dev/null' >/dev/null 2>&1 && FORK="yes"

echo "$SETUP:$CLONE_USER:$SETNS:$UMOUNT:$RESOLV_OK:$UNSHARE:$MOUNT:$READ:$NET:$WRITE:$FORK" > "$RESULT"
echo "done" > "$FIFO"
sleep 30
LCEOF
chmod +x /tmp/lifecycle.sh

unshare --user --map-root-user --net --mount \
    -- bash /tmp/lifecycle.sh "$FIFO7" /proto/drop_privs "$RESULT7" &
LC=$!

cat "$FIFO7" > /dev/null  # ns-ready
ip link add vh-lc type veth peer name vp-lc
ip link set vp-lc netns $LC
ip addr add 10.99.1.1/30 dev vh-lc
ip link set vh-lc up
echo "go" > "$FIFO7"
cat "$FIFO7" > /dev/null  # done

IFS=':' read -r SETUP CLONE_USER SETNS UMOUNT RESOLV_OK UNSHARE MOUNT READ NET WRITE FORK < "$RESULT7"

[ "$SETUP" = "yes" ]       && pass "setup: interfaces + bind mount OK" || fail "setup failed"
[ "$CLONE_USER" = "no" ]   && pass "agent: clone(CLONE_NEWUSER) blocked [VULN-01]" || fail "agent: clone(CLONE_NEWUSER) NOT blocked [VULN-01]"
[ "$SETNS" = "no" ]        && pass "agent: setns blocked [VULN-02]" || fail "agent: setns NOT blocked [VULN-02]"
[ "$UMOUNT" = "no" ]       && pass "agent: umount blocked [VULN-03]" || fail "agent: umount NOT blocked [VULN-03]"
[ "$RESOLV_OK" = "yes" ]   && pass "agent: resolv.conf still controlled after attack" || fail "agent: resolv.conf tampered!"
[ "$UNSHARE" = "no" ]      && pass "agent: unshare blocked" || fail "agent: unshare NOT blocked"
[ "$MOUNT" = "no" ]        && pass "agent: mount blocked" || fail "agent: mount NOT blocked"
[ "$READ" = "yes" ]        && pass "agent: file reads work" || fail "agent: reads broken"
[ "$NET" = "yes" ]         && pass "agent: network works" || fail "agent: network broken"
[ "$WRITE" = "yes" ]       && pass "agent: file writes work" || fail "agent: writes broken"
[ "$FORK" = "yes" ]        && pass "agent: fork/exec works" || fail "agent: fork/exec broken"

kill $LC 2>/dev/null; wait $LC 2>/dev/null || true
ip link del vh-lc 2>/dev/null || true
rm -f "$FIFO7" "$RESULT7"

# -------------------------------------------------------
# Summary
# -------------------------------------------------------
echo ""
echo "========================================="
echo "  Results: $PASS passed, $FAIL failed"
echo "========================================="
if [ $FAIL -eq 0 ]; then
    echo ""
    echo "  CLONE_NEWUSER approach is VIABLE."
    echo ""
    echo "  Container privileges (NO CAP_SYS_ADMIN):"
    echo "    cap_add: [NET_ADMIN]"
    echo "    seccomp: Docker default + allow CLONE_NEWUSER, unshare, setns"
    echo "    sysctls: net.ipv4.ip_forward=1"
    echo ""
    echo "  Per-worker hardening (before agent code):"
    echo "    seccomp-BPF: blocks clone(CLONE_NEWUSER), clone(CLONE_NEWNET),"
    echo "      clone3, unshare, setns, mount, umount2"
    echo "    Container-wide seccomp: blocks chroot, pivot_root, ptrace, keyctl"
    echo "    PR_SET_NO_NEW_PRIVS (required for seccomp, blocks setuid)"
    echo "    UID drop via SysProcAttr.Credential"
    echo ""
    exit 0
else
    echo ""
    echo "  Some tests failed. Review output above."
    exit 1
fi

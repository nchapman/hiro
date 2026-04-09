//go:build linux

package agent

import (
	"os/exec"
	"syscall"
)

// setNetworkCloneflags configures the command to spawn in its own user, network,
// and mount namespaces. CLONE_NEWUSER wraps the other namespace flags, giving the
// child full capabilities inside its user namespace without requiring CAP_SYS_ADMIN
// on the container.
//
// UID mapping: root (0) inside → agent UID outside.
// GID mappings: root (0) inside → primary GID outside, plus each supplementary GID
// mapped to itself so the child can call setgroups() for group-based filesystem access
// (e.g., hiro-operators for agents/ and skills/ write access).
func setNetworkCloneflags(cmd *exec.Cmd, uid, gid uint32, groups []uint32) {
	cmd.SysProcAttr.Cloneflags = syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET | syscall.CLONE_NEWNS
	cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{
		{ContainerID: 0, HostID: int(uid), Size: 1}, //nolint:gosec // UID fits int on 64-bit
	}

	// Map primary GID as root (0) inside, plus each supplementary GID to itself.
	gidMaps := []syscall.SysProcIDMap{
		{ContainerID: 0, HostID: int(gid), Size: 1}, //nolint:gosec // GID fits int on 64-bit
	}
	seen := map[uint32]bool{gid: true}
	for _, g := range groups {
		if !seen[g] {
			seen[g] = true
			gidMaps = append(gidMaps, syscall.SysProcIDMap{
				ContainerID: int(g), HostID: int(g), Size: 1, //nolint:gosec // GID fits int on 64-bit
			})
		}
	}
	cmd.SysProcAttr.GidMappings = gidMaps
}

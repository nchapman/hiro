//go:build linux

package agent

import (
	"os/exec"
	"syscall"
)

// setNetworkCloneflags configures the command to spawn in its own user, network,
// and mount namespaces. CLONE_NEWUSER wraps the other namespace flags, giving the
// child full capabilities inside its user namespace without requiring CAP_SYS_ADMIN
// on the container. The UID/GID mappings map root (0) inside the namespace to the
// agent's UID/GID outside.
func setNetworkCloneflags(cmd *exec.Cmd, uid, gid uint32) {
	cmd.SysProcAttr.Cloneflags = syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET | syscall.CLONE_NEWNS
	cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{
		{ContainerID: 0, HostID: int(uid), Size: 1}, //nolint:gosec // UID fits int on 64-bit
	}
	cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{
		{ContainerID: 0, HostID: int(gid), Size: 1}, //nolint:gosec // GID fits int on 64-bit
	}
}

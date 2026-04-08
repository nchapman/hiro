//go:build linux

package agent

import (
	"os/exec"
	"syscall"
)

// setNetworkCloneflags adds CLONE_NEWNET and CLONE_NEWNS to the command's
// SysProcAttr, placing the worker in its own network and mount namespaces.
func setNetworkCloneflags(cmd *exec.Cmd) {
	cmd.SysProcAttr.Cloneflags = syscall.CLONE_NEWNET | syscall.CLONE_NEWNS
}

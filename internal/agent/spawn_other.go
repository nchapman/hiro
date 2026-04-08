//go:build !linux

package agent

import "os/exec"

// setNetworkCloneflags is a no-op on non-Linux platforms.
// Network namespace isolation requires Linux.
func setNetworkCloneflags(_ *exec.Cmd) {}

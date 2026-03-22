//go:build isolation && unix

package agent

import (
	"os"
	"syscall"
)

func fileUIDPlatform(info os.FileInfo) (fileOwnership, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileOwnership{}, false
	}
	return fileOwnership{uid: stat.Uid, gid: stat.Gid}, true
}

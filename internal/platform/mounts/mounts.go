// Package mounts discovers filesystem mounts exposed to agents.
//
// Users expose host directories to agents by bind-mounting them into the Hiro
// container under <root>/mounts/<name>. Each subdirectory found there becomes
// a Mount with a probed read/write mode. The control plane adds these paths
// to the Landlock whitelist at spawn time and announces them to the agent via
// the MountProvider context provider (so the prompt cache is preserved when
// the mount set doesn't change).
//
// Mounts live as a sibling of workspace/ (not inside it) so they stay out of
// the cluster file-sync path — host-specific bind mounts should not be
// replicated across nodes.
//
// This is intentionally convention-based: there is no mount declaration file.
// Docker (or any other mechanism that makes a directory appear under mounts/)
// is the source of truth. Network volume types can be added later as plugins
// that mount into the same directory and get picked up by the same discovery.
package mounts

import (
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/sys/unix"
)

// Mode is whether a mount is writable by agents.
type Mode string

const (
	ModeRW Mode = "rw"
	ModeRO Mode = "ro"
)

// Mount is a discovered directory under <root>/mounts/.
type Mount struct {
	Name string // directory name (also the mount identifier)
	Path string // absolute path
	Mode Mode
}

// Dir returns the directory where mounts live for a given platform root.
func Dir(rootDir string) string {
	return filepath.Join(rootDir, "mounts")
}

// Discover scans <rootDir>/mounts/ for subdirectories and returns them as
// Mounts, sorted by name. Each mount's mode is determined by probing write
// access. Missing or empty mounts directory returns nil, nil.
func Discover(rootDir string) ([]Mount, error) {
	dir := Dir(rootDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var mounts []Mount
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Skip hidden entries (e.g. .DS_Store if somehow a dir, or .gitkeep).
		if e.Name() != "" && e.Name()[0] == '.' {
			continue
		}
		path := filepath.Join(dir, e.Name())
		mounts = append(mounts, Mount{
			Name: e.Name(),
			Path: path,
			Mode: probeMode(path),
		})
	}

	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Name < mounts[j].Name })
	return mounts, nil
}

// probeMode asks the kernel whether the current process could write to path,
// via access(2) with W_OK. No file is created, so there is no cleanup risk
// and nothing is left behind on the host filesystem. Read-only bind mounts
// return EROFS; permission-denied dirs return EACCES. Either way: ModeRO.
func probeMode(path string) Mode {
	if err := unix.Access(path, unix.W_OK); err != nil {
		return ModeRO
	}
	return ModeRW
}

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// allowedRoots defines the directory prefixes that file tools may access.
// Paths outside these roots are rejected. Set via SetAllowedRoots at startup.
var allowedRoots []string

// SetAllowedRoots configures the directories file tools may access.
// Must be called before any tool invocations. Paths should be absolute
// and cleaned. Typically: the platform root (/hive) and any instance dirs.
func SetAllowedRoots(roots []string) {
	allowedRoots = roots
}

// resolvePath resolves a potentially relative path against the working directory.
// Absolute paths are returned unchanged.
func resolvePath(workingDir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(workingDir, path))
}

// resolveAndConfine resolves a path and verifies it falls within allowed roots.
// Returns an error if the path escapes the allowed directories.
func resolveAndConfine(workingDir, path string) (string, error) {
	resolved := resolvePath(workingDir, path)

	// If no roots are configured, allow everything (non-isolated mode).
	if len(allowedRoots) == 0 {
		return resolved, nil
	}

	for _, root := range allowedRoots {
		if resolved == root || strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return resolved, nil
		}
	}

	return "", fmt.Errorf("access denied: %s is outside the allowed workspace", path)
}

// mkdirFor creates parent directories for a file path.
func mkdirFor(filePath string) error {
	dir := filepath.Dir(filePath)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0755)
}

// excludedDirs lists directories that tools should skip when walking
// file trees. Used by list_files, glob, grep, and ripgrep.
var excludedDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"__pycache__":  true,
	".git":         true,
}

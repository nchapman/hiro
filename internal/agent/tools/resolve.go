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
// Both the lexical path and its symlink-resolved real path must be inside the
// allowed roots. This prevents symlink-based escapes.
func resolveAndConfine(workingDir, path string) (string, error) {
	resolved := resolvePath(workingDir, path)

	// If no roots are configured, allow everything (non-isolated mode).
	if len(allowedRoots) == 0 {
		return resolved, nil
	}

	// Lexical check: the cleaned path must be within a root.
	if !isInsideRoots(resolved) {
		return "", fmt.Errorf("access denied: %s is outside the allowed workspace", path)
	}

	// Symlink check: resolve symlinks and re-validate to prevent symlink
	// escapes. If the resolved real path is still inside roots, allow it.
	// If the path doesn't exist yet, skip the symlink check — there's
	// nothing on disk to exploit.
	real, err := filepath.EvalSymlinks(resolved)
	if err == nil && !isInsideRoots(real) {
		return "", fmt.Errorf("access denied: %s resolves outside the allowed workspace via symlink", path)
	}

	return resolved, nil
}

// isInsideRoots reports whether path is equal to or under any allowed root.
// Resolves symlinks in roots to handle systems where temp dirs are symlinked
// (e.g., macOS /var → /private/var).
func isInsideRoots(path string) bool {
	for _, root := range allowedRoots {
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			return true
		}
		// Also check with the root's real path (handles symlinked roots).
		if realRoot, err := filepath.EvalSymlinks(root); err == nil && realRoot != root {
			if path == realRoot || strings.HasPrefix(path, realRoot+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// mkdirFor creates parent directories for a file path.
func mkdirFor(filePath string) error {
	dir := filepath.Dir(filePath)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0755)
}

// atomicWriteFile writes content to path via a temp file + rename so
// concurrent readers never see partial content.
func atomicWriteFile(path string, content []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".hive-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := f.Chmod(mode); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
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

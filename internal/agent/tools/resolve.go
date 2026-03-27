package tools

import (
	"os"
	"path/filepath"
)

// resolvePath resolves a potentially relative path against the working directory.
// Absolute paths are returned unchanged.
func resolvePath(workingDir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(workingDir, path)
}

// mkdirFor creates parent directories for a file path.
func mkdirFor(filePath string) error {
	dir := filepath.Dir(filePath)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0777)
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

package tools

import "path/filepath"

// resolvePath resolves a potentially relative path against the working directory.
// Absolute paths are returned unchanged.
func resolvePath(workingDir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(workingDir, path)
}

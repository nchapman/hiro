package tools

import (
	"os"
	"path/filepath"
)

// ForbiddenPaths holds absolute paths that file tools should hide from agents.
// This is a convenience filter, not a security boundary — it prevents agents
// from wasting time on files they shouldn't be editing (e.g. the control plane
// config). Actual access control is enforced at the OS level.
var ForbiddenPaths []string

// IsForbiddenPath returns true if the resolved absolute path matches
// any entry in ForbiddenPaths. Symlinks are resolved so that indirect
// paths are also filtered.
func IsForbiddenPath(resolved string) bool {
	real, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		// File may not exist yet (write_file creating a new file).
		// Fall back to the cleaned lexical path.
		real = filepath.Clean(resolved)
	}
	for _, fp := range ForbiddenPaths {
		if real == fp {
			return true
		}
	}
	return false
}

// resolvePath resolves a potentially relative path against the working directory.
// Absolute paths are returned unchanged.
func resolvePath(workingDir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(workingDir, path)
}

// filterForbiddenResults removes forbidden paths from search results so
// agents don't see files they shouldn't be interacting with.
// Paths may be relative to baseDir or absolute.
func filterForbiddenResults(paths []string, baseDir string) []string {
	if len(ForbiddenPaths) == 0 {
		return paths
	}
	filtered := make([]string, 0, len(paths))
	for _, p := range paths {
		abs := p
		if !filepath.IsAbs(p) {
			abs = filepath.Join(baseDir, p)
		}
		if !IsForbiddenPath(abs) {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

// mkdirFor creates parent directories for a file path.
func mkdirFor(filePath string) error {
	dir := filepath.Dir(filePath)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0777)
}

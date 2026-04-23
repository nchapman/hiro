package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/nchapman/hiro/internal/platform/fsperm"
)

// File-tool access is split along read/write lines so the in-process guard
// matches the Landlock ruleset: paths that are RO in the policy are reachable
// by Read/Glob/Grep but not Write/Edit. When Landlock is unavailable this is
// the only filesystem confinement the worker has, so the split matters.
var (
	readableRoots atomic.Value // holds []string
	writableRoots atomic.Value // holds []string
)

func loadRoots(v *atomic.Value) []string {
	raw := v.Load()
	if raw == nil {
		return nil
	}
	s, _ := raw.([]string)
	if len(s) == 0 {
		return nil
	}
	return s
}

// SetAllowedRoots is a convenience that configures the same list for both
// read and write access. Suitable for tests and non-isolated setups. Workers
// configured from the policy should call SetReadableRoots and SetWritableRoots
// separately so RO paths can't be written through file tools.
func SetAllowedRoots(roots []string) {
	SetReadableRoots(roots)
	SetWritableRoots(roots)
}

// SetReadableRoots configures paths that Read, Glob, and Grep may address.
func SetReadableRoots(roots []string) {
	if roots == nil {
		roots = []string{}
	}
	readableRoots.Store(roots)
}

// SetWritableRoots configures paths that Write and Edit may address.
func SetWritableRoots(roots []string) {
	if roots == nil {
		roots = []string{}
	}
	writableRoots.Store(roots)
}

// resolvePath resolves a potentially relative path against the working directory.
// Absolute paths are returned unchanged.
func resolvePath(workingDir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(workingDir, path))
}

// resolveForRead resolves a path for Read/Glob/Grep. Allows paths under any
// readable root.
func resolveForRead(workingDir, path string) (string, error) {
	return confineTo(workingDir, path, loadRoots(&readableRoots))
}

// resolveForWrite resolves a path for Write/Edit. Allows paths under any
// writable root only — RO paths from the policy pass readable checks but
// fail here.
func resolveForWrite(workingDir, path string) (string, error) {
	return confineTo(workingDir, path, loadRoots(&writableRoots))
}

// confineTo is the shared path-check used by both read and write resolvers.
// An empty roots slice means "no confinement" (non-isolated mode for tests).
func confineTo(workingDir, path string, roots []string) (string, error) {
	resolved := resolvePath(workingDir, path)
	if len(roots) == 0 {
		return resolved, nil
	}

	if !isInsideRoots(resolved, roots) {
		return "", fmt.Errorf("access denied: %s is outside the allowed workspace", path)
	}

	// Symlink re-check prevents escape via a symlink. If the target exists,
	// resolve it and verify the real path stays inside roots. If the target
	// doesn't exist yet (a typical Write-of-new-file case), resolve the
	// nearest existing ancestor — an agent could have created a symlink
	// component earlier (e.g. workspace/link → /etc) and then called
	// Write("workspace/link/shadow"). EvalSymlinks(target) would fail,
	// leaving the attack undetected at this layer. Landlock catches it on
	// Linux, but on non-Landlock platforms this in-process guard is the
	// only remaining check.
	if realPath, err := filepath.EvalSymlinks(resolved); err == nil {
		if !isInsideRoots(realPath, roots) {
			return "", fmt.Errorf("access denied: %s resolves outside the allowed workspace via symlink", path)
		}
	} else if lexAncestor, realAncestor, ok := nearestInsideAncestor(resolved, roots); ok && lexAncestor != realAncestor {
		// The leaf doesn't exist yet, but an existing ancestor that sits
		// inside the roots is a symlink (lexical != real). Check whether the
		// real path escapes the roots. This blocks the attack where an agent
		// creates workspace/link → /etc and then writes workspace/link/shadow
		// before the leaf exists.
		if !isInsideRoots(realAncestor, roots) {
			return "", fmt.Errorf("access denied: %s has an ancestor that resolves outside the allowed workspace via symlink", path)
		}
	}
	return resolved, nil
}

// nearestInsideAncestor walks up path looking for an existing directory that
// is lexically inside the given roots. Returns the lexical path and its
// symlink-resolved real path. Walking stops when we reach an ancestor that
// is outside the roots — those are parents-of-roots and not our concern.
// If no inside ancestor exists on disk (path is purely fictional), ok=false.
func nearestInsideAncestor(path string, roots []string) (lex, resolved string, ok bool) {
	for cur := filepath.Dir(path); cur != "" && cur != "."; cur = filepath.Dir(cur) {
		if !isInsideRoots(cur, roots) {
			return "", "", false
		}
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return cur, resolved, true
		}
		if cur == string(filepath.Separator) {
			break
		}
	}
	return "", "", false
}

// isInsideRoots reports whether path is equal to or under any allowed root.
// Resolves symlinks in roots to handle systems where temp dirs are symlinked
// (e.g., macOS /var → /private/var).
func isInsideRoots(path string, roots []string) bool {
	for _, root := range roots {
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
	return os.MkdirAll(dir, fsperm.DirStandard)
}

// atomicWriteFile writes content to path via a temp file + rename so
// concurrent readers never see partial content.
func atomicWriteFile(path string, content []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".hiro-tmp-*")
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
// file trees. Used by Glob, Grep, and ripgrep.
var excludedDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"__pycache__":  true,
	".git":         true,
}

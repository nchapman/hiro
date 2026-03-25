package api

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxFileSize = 2 << 20 // 2 MB

// resolveWorkspacePath validates and resolves a relative path within the
// workspace directory. It returns an error if the resolved path escapes the
// workspace root (path traversal), including via symlinks.
func resolveWorkspacePath(rootDir, relPath string) (string, error) {
	wsRoot := filepath.Join(rootDir, "workspace")
	if relPath == "" {
		return wsRoot, nil
	}
	joined := filepath.Join(wsRoot, relPath)
	cleaned := filepath.Clean(joined)
	// Lexical check: ensure the cleaned path is within the workspace root.
	if !strings.HasPrefix(cleaned, wsRoot+string(filepath.Separator)) && cleaned != wsRoot {
		return "", fmt.Errorf("path escapes workspace")
	}
	// Resolve the workspace root once (it may itself be a symlink mount).
	realRoot, err := filepath.EvalSymlinks(wsRoot)
	if err != nil {
		return "", fmt.Errorf("workspace root resolution failed: %w", err)
	}

	// Symlink check: resolve symlinks and re-validate to prevent escape.
	real, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("path resolution failed: %w", err)
		}
		// Path doesn't exist yet (e.g. write target). Walk up to the
		// deepest existing ancestor and verify it doesn't escape via
		// symlink, closing the TOCTOU window for partial-path attacks.
		ancestor := filepath.Dir(cleaned)
		for ancestor != wsRoot && ancestor != filepath.Dir(ancestor) {
			if _, statErr := os.Lstat(ancestor); statErr == nil {
				break
			}
			ancestor = filepath.Dir(ancestor)
		}
		realAncestor, err := filepath.EvalSymlinks(ancestor)
		if err != nil {
			return "", fmt.Errorf("path resolution failed: %w", err)
		}
		if !strings.HasPrefix(realAncestor, realRoot+string(filepath.Separator)) && realAncestor != realRoot {
			return "", fmt.Errorf("path escapes workspace")
		}
		return cleaned, nil
	}

	if !strings.HasPrefix(real, realRoot+string(filepath.Separator)) && real != realRoot {
		return "", fmt.Errorf("path escapes workspace")
	}
	return real, nil
}

type treeEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"` // "file" or "dir"
	Size int64  `json:"size,omitempty"`
}

func (s *Server) handleWorkspaceTree(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	absPath, err := resolveWorkspacePath(s.rootDir, relPath)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "directory not found", http.StatusNotFound)
			return
		}
		s.logger.Error("workspace tree read failed", "path", absPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	wsRoot := filepath.Join(s.rootDir, "workspace")
	result := make([]treeEntry, 0, len(entries))
	truncated := false
	for _, e := range entries {
		// Skip hidden files and symlinks.
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		entryAbs := filepath.Join(absPath, e.Name())
		entryRel, _ := filepath.Rel(wsRoot, entryAbs)

		te := treeEntry{
			Name: e.Name(),
			Path: entryRel,
		}
		if e.IsDir() {
			te.Type = "dir"
		} else {
			te.Type = "file"
			if info, err := e.Info(); err == nil {
				te.Size = info.Size()
			}
		}
		result = append(result, te)
		if len(result) >= 1000 {
			truncated = true
			break
		}
	}

	// Sort: directories first, then alphabetical.
	sort.Slice(result, func(i, j int) bool {
		if result[i].Type != result[j].Type {
			return result[i].Type == "dir"
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})

	if truncated {
		w.Header().Set("X-Truncated", "true")
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleWorkspaceFileRead(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "path parameter required", http.StatusBadRequest)
		return
	}
	absPath, err := resolveWorkspacePath(s.rootDir, relPath)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		s.logger.Error("workspace file stat failed", "path", absPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}
	if info.Size() > maxFileSize {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		s.logger.Error("workspace file read failed", "path", absPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleWorkspaceFileWrite(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "path parameter required", http.StatusBadRequest)
		return
	}
	absPath, err := resolveWorkspacePath(s.rootDir, relPath)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxFileSize+1))
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	if int64(len(body)) > maxFileSize {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	// Create parent directories if needed. Use 02775 (setgid + group-writable)
	// so agent processes (hive-agents group) can also write into these dirs.
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 02775); err != nil {
		s.logger.Error("workspace mkdir failed", "path", dir, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := os.WriteFile(absPath, body, 0664); err != nil {
		s.logger.Error("workspace file write failed", "path", absPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nchapman/hiro/internal/platform/fsperm"
	"github.com/nchapman/hiro/internal/watcher"
)

const maxFileReadSize = 2 << 20   // 2 MB — fits comfortably in the browser editor
const maxFileWriteSize = 50 << 20 // 50 MB — generous limit for drag-and-drop uploads

// maxTreeEntries caps the number of entries returned by the file tree endpoint.
const maxTreeEntries = 1000

// watchEventBufferSize is the channel buffer for file watch SSE subscribers.
const watchEventBufferSize = 16

// resolveFilesPath validates and resolves a relative path within the
// platform root directory. It returns an error if the resolved path escapes
// the root (path traversal), including via symlinks.
func resolveFilesPath(rootDir, relPath string) (string, error) {
	// Resolve the root once up front so all checks use the canonical path.
	realRoot, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		return "", fmt.Errorf("root resolution failed: %w", err)
	}

	if relPath == "" {
		return realRoot, nil
	}
	joined := filepath.Join(realRoot, relPath)
	cleaned := filepath.Clean(joined)
	// Lexical check: ensure the cleaned path is within the root.
	if !strings.HasPrefix(cleaned, realRoot+string(filepath.Separator)) && cleaned != realRoot {
		return "", fmt.Errorf("path escapes root")
	}

	// Symlink check: resolve symlinks and re-validate to prevent escape.
	realPath, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("path resolution failed: %w", err)
		}
		// Path doesn't exist yet (e.g. write target). Walk up to the
		// deepest existing ancestor and verify it doesn't escape via
		// symlink, closing the TOCTOU window for partial-path attacks.
		ancestor := filepath.Dir(cleaned)
		for ancestor != realRoot && ancestor != filepath.Dir(ancestor) {
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
			return "", fmt.Errorf("path escapes root")
		}
		return cleaned, nil
	}

	if !strings.HasPrefix(realPath, realRoot+string(filepath.Separator)) && realPath != realRoot {
		return "", fmt.Errorf("path escapes root")
	}
	return realPath, nil
}

// protectedPaths are platform-critical paths (relative to root) that cannot
// be deleted or renamed through the file browser API.
var protectedPaths = map[string]bool{
	"agents":    true,
	"instances": true,
	"skills":    true,
	"workspace": true,
	"config":    true,
}

// isProtectedPath returns true if absPath is a platform-critical path that
// should not be deleted or renamed.
func isProtectedPath(rootDir, absPath string) bool {
	rel, err := filepath.Rel(rootDir, absPath)
	if err != nil {
		return false
	}
	// Protect exact top-level dirs and anything under config/ (contains secrets).
	if protectedPaths[rel] {
		return true
	}
	for p := rel; p != "." && p != ""; p = filepath.Dir(p) {
		if p == "config" {
			return true
		}
	}
	return false
}

type treeEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"` // "file" or "dir"
	Size int64  `json:"size,omitempty"`
}

func (s *Server) handleFilesTree(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	absPath, err := resolveFilesPath(s.rootDir, relPath)
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
		s.logger.Error("files tree read failed", "path", absPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Resolve the root so Rel produces clean paths (absPath is already resolved).
	realRoot, err := filepath.EvalSymlinks(s.rootDir)
	if err != nil {
		s.logger.Error("root resolution failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	result := make([]treeEntry, 0, len(entries))
	truncated := false
	for _, e := range entries {
		// Skip hidden files (dot-prefix) and symlinks. At the platform root
		// this also prevents exposing .env, .git, and other sensitive paths.
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		entryAbs := filepath.Join(absPath, e.Name())
		entryRel, _ := filepath.Rel(realRoot, entryAbs)

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
		if len(result) >= maxTreeEntries {
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

func (s *Server) handleFilesFileRead(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "path parameter required", http.StatusBadRequest)
		return
	}
	absPath, err := resolveFilesPath(s.rootDir, relPath)
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
		s.logger.Error("files file stat failed", "path", absPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}
	if info.Size() > maxFileReadSize {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}

	// Use ServeContent (not ServeFile) to avoid redirect behavior that
	// breaks query-parameter-based routing. ServeContent still handles
	// Content-Type detection, range requests, and caching headers.
	f, err := os.Open(absPath)
	if err != nil {
		s.logger.Error("files file open failed", "path", absPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	http.ServeContent(w, r, filepath.Base(absPath), info.ModTime(), f)
}

func (s *Server) handleFilesFileWrite(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "path parameter required", http.StatusBadRequest)
		return
	}
	absPath, err := resolveFilesPath(s.rootDir, relPath)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	// Create parent directories if needed. Use 0o2775 (setgid + group-writable)
	// so agent processes (hiro-agents group) can also write into these dirs.
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, fsperm.DirSetgid); err != nil {
		s.logger.Error("files mkdir failed", "path", dir, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Stream to a temp file to avoid buffering large uploads in RAM.
	// The temp file lives in the same directory so os.Rename is atomic.
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		s.logger.Error("files upload temp create failed", "path", dir, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		if tmpName != "" {
			os.Remove(tmpName)
		}
	}()

	n, err := io.Copy(tmp, io.LimitReader(r.Body, maxFileWriteSize+1))
	if err != nil {
		s.logger.Error("files upload copy failed", "path", absPath, "error", err)
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	if n > maxFileWriteSize {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := tmp.Chmod(fsperm.FileCollaborative); err != nil {
		s.logger.Error("files upload chmod failed", "path", tmpName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	tmp.Close()

	if err := os.Rename(tmpName, absPath); err != nil {
		s.logger.Error("files file write failed", "path", absPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	tmpName = "" // disarm deferred remove

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFilesMkdir(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "path parameter required", http.StatusBadRequest)
		return
	}
	absPath, err := resolveFilesPath(s.rootDir, relPath)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	if err := os.MkdirAll(absPath, fsperm.DirSetgid); err != nil {
		s.logger.Error("files mkdir failed", "path", absPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFilesDelete(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "path parameter required", http.StatusBadRequest)
		return
	}
	absPath, err := resolveFilesPath(s.rootDir, relPath)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	// Don't allow deleting the platform root or top-level directories
	// (agents/, sessions/, skills/, etc.) to prevent accidental data loss.
	realRoot, err := filepath.EvalSymlinks(s.rootDir)
	if err != nil {
		s.logger.Error("root resolution failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if absPath == s.rootDir || absPath == realRoot {
		http.Error(w, "cannot delete root", http.StatusForbidden)
		return
	}
	if isProtectedPath(realRoot, absPath) {
		http.Error(w, "cannot delete protected path", http.StatusForbidden)
		return
	}

	// Re-check for symlink substitution between resolve and delete to
	// close the TOCTOU window where an agent swaps the path for a symlink.
	linfo, err := os.Lstat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.logger.Error("files delete lstat failed", "path", absPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if linfo.Mode()&os.ModeSymlink != 0 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	if err := os.RemoveAll(absPath); err != nil {
		s.logger.Error("files delete failed", "path", absPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFilesRename(w http.ResponseWriter, r *http.Request) {
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" || to == "" {
		http.Error(w, "from and to parameters required", http.StatusBadRequest)
		return
	}
	absFrom, err := resolveFilesPath(s.rootDir, from)
	if err != nil {
		http.Error(w, "invalid source path", http.StatusBadRequest)
		return
	}
	absTo, err := resolveFilesPath(s.rootDir, to)
	if err != nil {
		http.Error(w, "invalid destination path", http.StatusBadRequest)
		return
	}

	// Don't allow renaming protected platform paths.
	// Use the resolved root since resolveFilesPath returns resolved paths.
	realRoot, err := filepath.EvalSymlinks(s.rootDir)
	if err != nil {
		s.logger.Error("root resolution failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if isProtectedPath(realRoot, absFrom) {
		http.Error(w, "cannot rename protected path", http.StatusForbidden)
		return
	}

	// Re-check source for symlink substitution (TOCTOU mitigation).
	if linfo, err := os.Lstat(absFrom); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "source not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if linfo.Mode()&os.ModeSymlink != 0 {
		http.Error(w, "invalid source path", http.StatusBadRequest)
		return
	}

	// Ensure destination doesn't already exist.
	if _, err := os.Stat(absTo); err == nil {
		http.Error(w, "destination already exists", http.StatusConflict)
		return
	}

	// Create parent dirs for destination if needed.
	if err := os.MkdirAll(filepath.Dir(absTo), fsperm.DirSetgid); err != nil {
		s.logger.Error("files rename mkdir failed", "path", absTo, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := os.Rename(absFrom, absTo); err != nil {
		s.logger.Error("files rename failed", "from", absFrom, "to", absTo, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Server-Sent Events for file watching ---

type fileEvent struct {
	Path string `json:"path"`
	Op   string `json:"op"` // "create", "write", "remove", "rename"
}

type fileEventBatch struct {
	Events []fileEvent `json:"events"`
}

// handleFilesWatch streams filesystem change events via Server-Sent Events.
// The watcher already debounces at 100ms, so each SSE message contains a
// batch of coalesced changes.
func (s *Server) handleFilesWatch(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	if s.watcher == nil {
		http.Error(w, "file watching not available", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable proxy buffering (Nginx etc.)
	flusher.Flush()

	// Channel bridges the watcher goroutine to the SSE write loop.
	ch := make(chan []watcher.Event, watchEventBufferSize)

	unsub := s.watcher.Subscribe("**", func(events []watcher.Event) {
		select {
		case ch <- events:
		default:
			s.logger.Debug("SSE client too slow, dropping file events", "count", len(events))
		}
	})
	defer unsub()

	for {
		select {
		case <-r.Context().Done():
			return
		case events := <-ch:
			batch := fileEventBatch{
				Events: make([]fileEvent, 0, len(events)),
			}
			for _, ev := range events {
				batch.Events = append(batch.Events, fileEvent{
					Path: ev.Path,
					Op:   ev.Op.String(),
				})
			}
			data, err := json.Marshal(batch)
			if err != nil {
				s.logger.Warn("failed to marshal file events", "error", err)
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

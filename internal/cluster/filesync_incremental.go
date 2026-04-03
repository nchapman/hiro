package cluster

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
)

// WatchAndSync starts watching the synced directories for changes and
// sends updates via sendFn. Blocks until Stop is called.
func (s *FileSyncService) WatchAndSync() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	// Add all synced directories recursively.
	for _, dir := range s.syncDirs {
		absDir := filepath.Join(s.rootDir, dir)
		if err = s.addWatchRecursive(watcher, absDir); err != nil {
			s.logger.Warn("failed to watch directory", "dir", dir, "error", err)
		}
	}

	// Debounce timer and pending changes.
	var mu sync.Mutex
	pending := make(map[string]bool) // path → deleted
	timer := time.NewTimer(debounceInterval)
	timer.Stop()

	for {
		select {
		case <-s.stopCh:
			return nil

		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			relPath, err := filepath.Rel(s.rootDir, event.Name)
			if err != nil || shouldIgnore(relPath) {
				continue
			}
			if s.isEchoSuppressed(relPath) {
				continue
			}

			// If a new directory was created, watch it recursively and
			// scan for files that were written before the watch was added.
			// This covers the race where mkdir + write happens atomically
			// (e.g., API file upload creates parent dirs then writes the file
			// before fsnotify processes the directory creation event).
			if event.Has(fsnotify.Create) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					_ = s.addWatchRecursive(watcher, event.Name)
					s.scanNewDir(event.Name, &mu, pending, timer)
				}
			}

			mu.Lock()
			deleted := event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename)
			pending[relPath] = deleted
			timer.Reset(debounceInterval)
			mu.Unlock()

		case <-timer.C:
			mu.Lock()
			batch := pending
			pending = make(map[string]bool)
			mu.Unlock()

			for relPath, deleted := range batch {
				if err := s.sendChange(relPath, deleted); err != nil {
					s.logger.Warn("failed to send file change", "path", relPath, "error", err)
				}
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			s.logger.Warn("watcher error", "error", err)
		}
	}
}

// sendChange reads a file and sends it as a FileUpdate.
func (s *FileSyncService) sendChange(relPath string, deleted bool) error {
	if deleted {
		return s.sendFn(&pb.FileUpdate{
			Path:       relPath,
			Deleted:    true,
			OriginNode: s.nodeID,
		})
	}

	absPath := filepath.Join(s.rootDir, relPath)
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			// File was deleted between event and read.
			return s.sendFn(&pb.FileUpdate{
				Path:       relPath,
				Deleted:    true,
				OriginNode: s.nodeID,
			})
		}
		return err
	}

	if info.IsDir() {
		return nil // directories are created implicitly
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if info.Size() > maxFileSize {
		s.logger.Warn("skipping large file sync", "path", relPath, "size", info.Size())
		return nil
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return err
	}

	return s.sendFn(&pb.FileUpdate{
		Path:           relPath,
		Content:        content,
		Mode:           uint32(info.Mode().Perm()),
		MtimeUnixNanos: info.ModTime().UnixNano(),
		OriginNode:     s.nodeID,
	})
}

// ApplyFileUpdate applies an incoming FileUpdate to the local filesystem.
// If a conflict is detected (local file was modified since the last known
// state), the local version is preserved as a .conflict file.
func (s *FileSyncService) ApplyFileUpdate(update *pb.FileUpdate) error {
	if shouldIgnore(update.Path) {
		return nil
	}

	absPath := filepath.Join(s.rootDir, update.Path)

	// Prevent path traversal.
	rel, relErr := filepath.Rel(s.rootDir, filepath.Clean(absPath))
	if relErr != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("path traversal rejected: %s", update.Path)
	}

	// Suppress echo so our write doesn't trigger a sync back.
	s.suppressEcho(update.Path)

	if update.Deleted {
		if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("deleting %s: %w", update.Path, err)
		}
		s.logger.Debug("file deleted via sync", "path", update.Path)
		return nil
	}

	// Conflict detection: a conflict is real only when the local file was
	// modified independently (local mtime > last-received mtime). This
	// avoids false positives from files we wrote ourselves via sync.
	if update.MtimeUnixNanos > 0 {
		if localInfo, err := os.Stat(absPath); err == nil {
			localMtime := localInfo.ModTime().UnixNano()

			s.receivedMu.Lock()
			lastReceived, tracked := s.receivedMtime[update.Path]
			s.receivedMu.Unlock()

			// If the file is tracked and local mtime exceeds the last
			// version we synced, a local write happened independently.
			if tracked && localMtime > lastReceived {
				conflictPath := fmt.Sprintf("%s.conflict.%s.%d",
					absPath, sanitizeNodeID(update.OriginNode), time.Now().Unix())
				s.logger.Warn("file conflict detected, preserving both versions",
					"path", update.Path,
					"local_mtime", localMtime,
					"last_received", lastReceived,
					"conflict_file", conflictPath,
				)
				// Save incoming version as the conflict file (local stays as-is).
				if err := os.WriteFile(conflictPath, update.Content, os.FileMode(update.Mode)); err != nil {
					s.logger.Warn("failed to write conflict file", "path", conflictPath, "error", err)
				}
				// Still update the received mtime so future updates don't
				// keep creating conflict files for the same divergence.
				s.receivedMu.Lock()
				s.receivedMtime[update.Path] = update.MtimeUnixNanos
				s.receivedMu.Unlock()
				return nil
			}
		}
	}

	// Write the file atomically (temp + rename) so concurrent readers
	// never see partial content.
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return fmt.Errorf("creating parent dir for %s: %w", update.Path, err)
	}

	mode := os.FileMode(0o644)
	if update.Mode != 0 {
		// Strip execute, setuid, setgid, and sticky bits from remote files
		// to prevent a compromised node from planting executables.
		mode = os.FileMode(update.Mode) & 0o666
	}

	if err := atomicWrite(absPath, update.Content, mode); err != nil {
		return fmt.Errorf("writing %s: %w", update.Path, err)
	}

	// Restore mtime and track it for conflict detection.
	if update.MtimeUnixNanos > 0 {
		mtime := time.Unix(0, update.MtimeUnixNanos)
		if err := os.Chtimes(absPath, mtime, mtime); err != nil {
			s.logger.Warn("failed to restore mtime", "path", update.Path, "error", err)
		}

		s.receivedMu.Lock()
		s.receivedMtime[update.Path] = update.MtimeUnixNanos
		s.receivedMu.Unlock()
	}

	s.logger.Debug("file synced", "path", update.Path, "size", len(update.Content))
	return nil
}

// suppressEcho marks a path as recently written so fsnotify events for it
// are ignored (preventing sync loops).
func (s *FileSyncService) suppressEcho(relPath string) {
	s.echoMu.Lock()
	s.echoPaths[relPath] = time.Now()
	s.echoMu.Unlock()
}

// isEchoSuppressed returns true if a path was recently written by us.
func (s *FileSyncService) isEchoSuppressed(relPath string) bool {
	s.echoMu.Lock()
	defer s.echoMu.Unlock()
	t, ok := s.echoPaths[relPath]
	if !ok {
		return false
	}
	if time.Since(t) > echoSuppressionTTL {
		delete(s.echoPaths, relPath)
		return false
	}
	return true
}

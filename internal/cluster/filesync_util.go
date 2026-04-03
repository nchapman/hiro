package cluster

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
)

// Reconcile performs a full directory walk and sends updates for any files
// that differ from the expected state. This covers:
//   - Files changed during initial sync (tar creation → extraction gap)
//   - Drift from missed fsnotify events
//   - Any other inconsistencies
//
// knownFiles maps relative paths to their mtime (UnixNano). Files not in
// knownFiles or with a different mtime are sent. Pass nil to send all files.
func (s *FileSyncService) Reconcile(knownFiles map[string]int64) error {
	seen := make(map[string]bool)

	for _, dir := range s.syncDirs {
		absDir := filepath.Join(s.rootDir, dir)
		if _, err := os.Stat(absDir); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(absDir, func(path string, d fs.DirEntry, err error) error {
			return s.reconcileEntry(path, d, err, knownFiles, seen)
		})
		if err != nil {
			return fmt.Errorf("reconcile walk %s: %w", dir, err)
		}
	}

	// Files in knownFiles but not on disk → deleted.
	for relPath := range knownFiles {
		if !seen[relPath] {
			if err := s.sendFn(&pb.FileUpdate{
				Path:       relPath,
				Deleted:    true,
				OriginNode: s.nodeID,
			}); err != nil {
				s.logger.Warn("reconcile: failed to send delete", "path", relPath, "error", err)
			}
		}
	}

	s.logger.Info("reconciliation complete", "files_checked", len(seen))
	return nil
}

// reconcileEntry processes a single entry during reconciliation, sending an
// update if the file differs from the known state.
func (s *FileSyncService) reconcileEntry(path string, d fs.DirEntry, walkErr error, knownFiles map[string]int64, seen map[string]bool) error {
	if walkErr != nil {
		return nil //nolint:nilerr // skip inaccessible entries
	}
	relPath, _ := filepath.Rel(s.rootDir, path)
	if shouldIgnore(relPath) {
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	if d.IsDir() {
		return nil
	}
	info, err := d.Info()
	if err != nil || !info.Mode().IsRegular() {
		return nil //nolint:nilerr // skip entries with unreadable metadata or non-regular files
	}
	if info.Size() > maxFileSize {
		return nil
	}

	seen[relPath] = true
	mtime := info.ModTime().UnixNano()

	// Skip if unchanged.
	if knownFiles != nil {
		if knownMtime, ok := knownFiles[relPath]; ok && knownMtime == mtime {
			return nil
		}
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil //nolint:nilerr // skip unreadable files
	}

	if err := s.sendFn(&pb.FileUpdate{
		Path:           relPath,
		Content:        content,
		Mode:           uint32(info.Mode().Perm()),
		MtimeUnixNanos: mtime,
		OriginNode:     s.nodeID,
	}); err != nil {
		s.logger.Warn("reconcile: failed to send update", "path", relPath, "error", err)
	}
	return nil
}

// addWatchRecursive adds fsnotify watches for a directory and all
// subdirectories, skipping ignored paths.
func (s *FileSyncService) addWatchRecursive(w *fsnotify.Watcher, dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip inaccessible directories
		}
		if !d.IsDir() {
			return nil
		}
		relPath, _ := filepath.Rel(s.rootDir, path)
		if shouldIgnore(relPath) {
			return filepath.SkipDir
		}
		if err := w.Add(path); err != nil {
			s.logger.Debug("failed to watch dir", "path", path, "error", err)
		}
		return nil
	})
}

// scanNewDir walks a newly created directory and adds any files found to the
// pending change set. This handles the race where files are written inside a
// new directory before fsnotify can register a watch on it.
func (s *FileSyncService) scanNewDir(dir string, mu *sync.Mutex, pending map[string]bool, timer *time.Timer) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // skip inaccessible entries and directories
		}
		relPath, err := filepath.Rel(s.rootDir, path)
		if err != nil || shouldIgnore(relPath) {
			return nil //nolint:nilerr // skip entries that can't be relativized or are ignored
		}
		if s.isEchoSuppressed(relPath) {
			return nil
		}
		mu.Lock()
		pending[relPath] = false
		timer.Reset(debounceInterval)
		mu.Unlock()
		return nil
	})
}

// atomicWrite writes content to path via a temp file + rename so concurrent
// readers never see partial content. The temp file uses a random suffix to
// prevent corruption when multiple goroutines write the same path concurrently.
// It is placed in the same directory to guarantee same-filesystem rename
// (which is atomic on POSIX).
func atomicWrite(path string, content []byte, mode os.FileMode) error {
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

// atomicWriteFromReader is like atomicWrite but reads from an io.Reader
// (used during tar extraction where content is streamed).
func atomicWriteFromReader(path string, r io.Reader, mode os.FileMode) error {
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
	if _, err := io.Copy(f, r); err != nil {
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

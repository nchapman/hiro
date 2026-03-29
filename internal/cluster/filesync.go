package cluster

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/klauspost/compress/zstd"
	pb "github.com/nchapman/hivebot/internal/ipc/proto"
)

const (
	// debounceInterval batches rapid filesystem events before syncing.
	debounceInterval = 200 * time.Millisecond

	// echoSuppressionTTL is how long we suppress fsnotify events for
	// files we just wrote ourselves (to prevent sync loops).
	echoSuppressionTTL = 1 * time.Second

	// maxFileSize is the maximum file size we'll sync inline.
	// Files larger than this are skipped with a warning.
	maxFileSize = 50 * 1024 * 1024 // 50MB

	// initialSyncChunkSize is the max size per FileSyncData message.
	initialSyncChunkSize = 1 * 1024 * 1024 // 1MB
)

// ignoredNames are directories/files that should never be synced.
var ignoredNames = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	".hive":        true,
	"__pycache__":  true,
	".DS_Store":    true,
}

// ignoredExtensions are file extensions that should never be synced.
var ignoredExtensions = map[string]bool{
	".swp": true,
	".swo": true,
	".tmp": true,
}

// shouldIgnore returns true if a relative path should be excluded from sync.
func shouldIgnore(relPath string) bool {
	parts := strings.Split(relPath, string(filepath.Separator))
	for _, part := range parts {
		if ignoredNames[part] {
			return true
		}
	}
	ext := filepath.Ext(relPath)
	if ignoredExtensions[ext] {
		return true
	}
	return false
}

// FileChange represents a single filesystem change to sync.
type FileChange struct {
	Path    string // relative path within the watched directory
	Deleted bool
}

// FileSyncService watches a directory for changes and sends/receives
// file updates over the cluster stream. It handles:
//   - Initial full sync (tar.gz) on node connect
//   - Incremental sync via fsnotify + debouncing
//   - Echo suppression to prevent sync loops
//   - Conflict preservation (never silently drops a version)
type FileSyncService struct {
	rootDir    string   // absolute path to the hive root
	syncDirs   []string // directories to sync (relative to rootDir)
	nodeID     string   // this node's ID (for conflict file naming)
	logger     *slog.Logger

	// sendFn is called to send a FileUpdate to the other side.
	sendFn func(update *pb.FileUpdate) error

	// Echo suppression: recently-written paths that should be ignored
	// by the fsnotify watcher to prevent sync loops.
	echoMu    sync.Mutex
	echoPaths map[string]time.Time

	// Last-received mtime tracking for conflict detection.
	// Maps relative paths to the mtime (UnixNano) of the last version
	// we received from sync. A conflict is real only when the local
	// mtime exceeds the last-received mtime (meaning a local write
	// happened independently).
	receivedMu    sync.Mutex
	receivedMtime map[string]int64

	// Stop signal.
	stopCh chan struct{}
	stopOnce sync.Once
}

// FileSyncConfig configures the file sync service.
type FileSyncConfig struct {
	RootDir  string   // absolute path to HIVE_ROOT
	SyncDirs []string // directories to sync (e.g., ["agents", "skills", "workspace"])
	NodeID   string   // this node's ID
	SendFn   func(update *pb.FileUpdate) error
	Logger   *slog.Logger
}

// NewFileSyncService creates a new file sync service.
func NewFileSyncService(cfg FileSyncConfig) *FileSyncService {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &FileSyncService{
		rootDir:       cfg.RootDir,
		syncDirs:      cfg.SyncDirs,
		nodeID:        cfg.NodeID,
		sendFn:        cfg.SendFn,
		logger:        logger,
		echoPaths:     make(map[string]time.Time),
		receivedMtime: make(map[string]int64),
		stopCh:        make(chan struct{}),
	}
}

// --- Initial sync (tar.gz) ---

// CreateInitialSync creates a zstd-compressed tar of all synced directories.
// Returns the compressed bytes.
func (s *FileSyncService) CreateInitialSync() ([]byte, error) {
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		return nil, fmt.Errorf("creating zstd writer: %w", err)
	}
	tw := tar.NewWriter(zw)

	for _, dir := range s.syncDirs {
		absDir := filepath.Join(s.rootDir, dir)
		if _, err := os.Stat(absDir); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(absDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip errors
			}
			relPath, _ := filepath.Rel(s.rootDir, path)
			if shouldIgnore(relPath) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}

			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return nil
			}
			header.Name = relPath

			if d.IsDir() {
				header.Name += "/"
				return tw.WriteHeader(header)
			}

			if !info.Mode().IsRegular() {
				return nil
			}
			if info.Size() > maxFileSize {
				s.logger.Warn("skipping large file in initial sync", "path", relPath, "size", info.Size())
				return nil
			}

			if err := tw.WriteHeader(header); err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("walking %s: %w", dir, err)
		}
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("closing tar: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("closing zstd: %w", err)
	}
	return buf.Bytes(), nil
}

// ApplyInitialSync extracts a zstd-compressed tar to the root directory.
func (s *FileSyncService) ApplyInitialSync(data []byte) error {
	zr, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("opening zstd: %w", err)
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		target := filepath.Join(s.rootDir, header.Name)

		// Prevent path traversal.
		if !strings.HasPrefix(target, s.rootDir+string(filepath.Separator)) && target != s.rootDir {
			continue
		}

		// Suppress echo for all extracted files.
		s.suppressEcho(header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("creating dir %s: %w", header.Name, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("creating file %s: %w", header.Name, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("writing file %s: %w", header.Name, err)
			}
			f.Close()
		}
	}

	s.logger.Info("initial sync applied", "root", s.rootDir)
	return nil
}

// --- Incremental sync ---

// WatchAndSync starts watching the synced directories for changes and
// sends updates via sendFn. Blocks until Stop is called.
func (s *FileSyncService) WatchAndSync() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating watcher: %w", err)
	}
	defer watcher.Close()

	// Add all synced directories recursively.
	for _, dir := range s.syncDirs {
		absDir := filepath.Join(s.rootDir, dir)
		if err := s.addWatchRecursive(watcher, absDir); err != nil {
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

			// If a new directory was created, watch it recursively.
			if event.Has(fsnotify.Create) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					s.addWatchRecursive(watcher, event.Name)
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
	if !strings.HasPrefix(absPath, s.rootDir+string(filepath.Separator)) {
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

	// Write the file.
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		return fmt.Errorf("creating parent dir for %s: %w", update.Path, err)
	}

	mode := os.FileMode(0644)
	if update.Mode != 0 {
		mode = os.FileMode(update.Mode)
	}

	if err := os.WriteFile(absPath, update.Content, mode); err != nil {
		return fmt.Errorf("writing %s: %w", update.Path, err)
	}

	// Restore mtime and track it for conflict detection.
	if update.MtimeUnixNanos > 0 {
		mtime := time.Unix(0, update.MtimeUnixNanos)
		os.Chtimes(absPath, mtime, mtime)

		s.receivedMu.Lock()
		s.receivedMtime[update.Path] = update.MtimeUnixNanos
		s.receivedMu.Unlock()
	}

	s.logger.Debug("file synced", "path", update.Path, "size", len(update.Content))
	return nil
}

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
			if err != nil {
				return nil
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
				return nil
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
				return nil
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
		})
		if err != nil {
			return fmt.Errorf("reconcile walk %s: %w", dir, err)
		}
	}

	// Files in knownFiles but not on disk → deleted.
	if knownFiles != nil {
		for relPath := range knownFiles {
			if !seen[relPath] {
				s.sendFn(&pb.FileUpdate{
					Path:       relPath,
					Deleted:    true,
					OriginNode: s.nodeID,
				})
			}
		}
	}

	s.logger.Info("reconciliation complete", "files_checked", len(seen))
	return nil
}

// Stop signals the watcher to shut down.
func (s *FileSyncService) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
}

// --- Echo suppression ---

func (s *FileSyncService) suppressEcho(relPath string) {
	s.echoMu.Lock()
	s.echoPaths[relPath] = time.Now()
	s.echoMu.Unlock()
}

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

// --- Helpers ---

// addWatchRecursive adds fsnotify watches for a directory and all
// subdirectories, skipping ignored paths.
func (s *FileSyncService) addWatchRecursive(w *fsnotify.Watcher, dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
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

// sanitizeNodeID removes characters that are invalid in filenames.
func sanitizeNodeID(id string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, id)
}

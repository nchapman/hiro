package cluster

import (
	"log/slog"
	"sync"
	"time"

	pb "github.com/nchapman/hivebot/internal/ipc/proto"
)

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
	rootDir  string   // absolute path to the hive root
	syncDirs []string // directories to sync (relative to rootDir)
	nodeID   string   // this node's ID (for conflict file naming)
	logger   *slog.Logger

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
	stopCh   chan struct{}
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

// Stop signals the watcher to shut down.
func (s *FileSyncService) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
}

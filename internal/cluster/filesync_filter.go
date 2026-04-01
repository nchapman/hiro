package cluster

import (
	"path/filepath"
	"strings"
	"time"
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
	".hiro":        true,
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
	base := filepath.Base(relPath)
	// Atomic write temp files (e.g. .hiro-tmp-123456789).
	if strings.HasPrefix(base, ".hiro-tmp-") {
		return true
	}
	ext := filepath.Ext(relPath)
	if ignoredExtensions[ext] {
		return true
	}
	return false
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

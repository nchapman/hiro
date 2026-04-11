package cluster

import (
	"bufio"
	"log/slog"
	"os"
	"path"
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

// .syncignore format
//
// Controls which files are excluded from cluster file sync. Place at the
// platform root (HIRO_ROOT). One pattern per line. Blank lines and lines
// starting with # are ignored. Hot-reloaded on change.
//
// If the file is missing or has no valid patterns, built-in defaults are
// used (see defaultIgnorePatterns below).
//
// This is NOT gitignore. The syntax is deliberately simpler:
//
//	Pattern type        Example              Behavior
//	──────────────────  ───────────────────  ─────────────────────────────────────
//	Bare name           node_modules         Matches any path component at any depth
//	Glob                *.log                Matches any component; * does not cross /
//	Path pattern        workspace/tmp/*.dat  Matched against the full relative path
//	Path prefix         workspace/logs       Also matches all children (workspace/logs/*)
//	Trailing slash      output/              Stripped; treated as bare name "output"
//	Bracket expression  [._]cache            Matches _cache, .cache; ranges like [a-z] work
//
// What is NOT supported (and will be warned about or rejected):
//
//   - ** (doublestar) — bare names already match at any depth
//   - ! (negation)
//   - \ (backslash escaping)
//   - Per-directory ignore files — single flat file only
//
// Wildcards use Go's path.Match semantics: * matches any sequence of
// non-separator characters, ? matches exactly one non-separator character.

// syncIgnorePattern is a single parsed pattern from .syncignore.
type syncIgnorePattern struct {
	pattern string
	isPath  bool // contains '/', matches against the full relative path
}

// defaultIgnorePatterns are used when no .syncignore file exists, or when
// the file exists but contains no valid patterns.
var defaultIgnorePatterns = []string{
	// Version control
	".git",

	// Dependencies
	"node_modules",
	"venv",
	".venv",

	// Build output
	"dist",

	// Platform internal
	".hiro",

	// Language caches
	"__pycache__",

	// OS metadata
	".DS_Store",
	"Thumbs.db",

	// Editor swap/temp/backup files
	"*.swp",
	"*.swo",
	"*.tmp",
	"*.bak",
	"*.orig",

	// Log files
	"*.log",
}

// loadSyncIgnore loads ignore patterns from rootDir/.syncignore.
// Falls back to defaultIgnorePatterns if the file doesn't exist or
// contains no valid patterns.
func loadSyncIgnore(rootDir string, logger *slog.Logger) []syncIgnorePattern {
	ignorePath := filepath.Join(rootDir, ".syncignore")
	f, err := os.Open(ignorePath) //nolint:gosec // path is rootDir/.syncignore, not user-controlled
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("failed to read .syncignore, using defaults", "error", err)
		}
		return parseIgnorePatterns(defaultIgnorePatterns, logger)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		logger.Warn("error reading .syncignore, using defaults", "error", err)
		return parseIgnorePatterns(defaultIgnorePatterns, logger)
	}

	patterns := parseIgnorePatterns(lines, logger)
	if len(patterns) == 0 {
		logger.Debug(".syncignore has no valid patterns, using defaults")
		return parseIgnorePatterns(defaultIgnorePatterns, logger)
	}

	logger.Debug("loaded .syncignore", "patterns", len(patterns))
	return patterns
}

// parseIgnorePatterns converts raw pattern strings into syncIgnorePattern
// values, validating each pattern and logging warnings for invalid ones.
func parseIgnorePatterns(lines []string, logger *slog.Logger) []syncIgnorePattern {
	patterns := make([]syncIgnorePattern, 0, len(lines))
	for _, line := range lines {
		// Strip trailing slash (directory hint) — we match components anyway.
		line = strings.TrimRight(line, "/")
		if line == "" {
			continue
		}
		// ** is not supported by path.Match. Warn and skip so users don't
		// get silent no-matches. Bare names (e.g. "node_modules") already
		// match at any depth via component matching.
		if strings.Contains(line, "**") {
			logger.Warn(".syncignore: ** not supported, use bare names for any-depth matching",
				"pattern", line)
			continue
		}
		// Validate the pattern is syntactically valid.
		if _, err := path.Match(line, ""); err != nil {
			logger.Warn(".syncignore: invalid pattern, skipping",
				"pattern", line, "error", err)
			continue
		}
		patterns = append(patterns, syncIgnorePattern{
			pattern: line,
			isPath:  strings.Contains(line, "/"),
		})
	}
	return patterns
}

// shouldIgnore returns true if a relative path should be excluded from sync.
// See the .syncignore format documentation at the top of this file.
func (s *FileSyncService) shouldIgnore(relPath string) bool {
	// Normalize to forward slashes for consistent matching across platforms.
	relPath = filepath.ToSlash(relPath)

	// Always ignore atomic write temp files (internal implementation detail).
	base := path.Base(relPath)
	if strings.HasPrefix(base, ".hiro-tmp-") {
		return true
	}

	patterns := s.ignorePatterns.Load()
	for _, p := range *patterns {
		if p.isPath {
			// Match against the full relative path.
			if matched, _ := path.Match(p.pattern, relPath); matched {
				return true
			}
			// Non-glob path patterns also match as directory prefixes.
			if !strings.ContainsAny(p.pattern, "*?[") &&
				strings.HasPrefix(relPath, p.pattern+"/") {
				return true
			}
			continue
		}
		// Match against each path component.
		for part := range strings.SplitSeq(relPath, "/") {
			if matched, _ := path.Match(p.pattern, part); matched {
				return true
			}
		}
	}
	return false
}

// reloadSyncIgnore reloads ignore patterns from .syncignore on disk.
func (s *FileSyncService) reloadSyncIgnore() {
	patterns := loadSyncIgnore(s.rootDir, s.logger)
	s.ignorePatterns.Store(&patterns)
	s.logger.Info("reloaded .syncignore", "patterns", len(patterns))
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

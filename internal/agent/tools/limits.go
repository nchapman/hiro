package tools

import "time"

// Resource limit constants for agent tools, organized by category.

// --- Timeouts ---

const (
	// bashTimeout is the maximum duration for synchronous bash commands.
	bashTimeout = 120 * time.Second

	// autoBackgroundAfter is when a bash command is automatically backgrounded.
	autoBackgroundAfter = 60 * time.Second

	// grepTimeout is the maximum duration for a grep search.
	grepTimeout = 30 * time.Second

	// fetchTimeout is the maximum duration for an HTTP fetch.
	fetchTimeout = 30 * time.Second

	// killTimeout bounds how long Kill waits for a job to exit.
	killTimeout = 5 * time.Second

	// waitDelay is how long to wait for pipe draining after process kill.
	waitDelay = 2 * time.Second
)

// --- Output and buffer sizes ---

const (
	// maxOutputLen is the maximum length of synchronous bash output (bytes).
	maxOutputLen = 32000

	// maxBufferBytes is the maximum size of each stdout/stderr buffer (4MB).
	maxBufferBytes = 4 << 20

	// maxResponseBody is the maximum size of an HTTP fetch response (bytes).
	maxResponseBody = 64000

	// maxFileReadLen is the maximum output length when reading a file (bytes).
	maxFileReadLen = 64000

	// maxRgOutputBytes caps ripgrep output to prevent runaway memory usage (64MB).
	maxRgOutputBytes = 64 * 1024 * 1024
)

// --- File size limits ---

const (
	// maxFileSize is the largest file we'll attempt to read into memory (10MB).
	maxFileSize = 10 * 1024 * 1024
)

// --- Result count limits ---

const (
	// maxGrepResults is the maximum number of grep matches returned.
	maxGrepResults = 100

	// maxGrepLineWidth is the maximum character width of a single grep match line.
	maxGrepLineWidth = 500

	// maxMatchesPerFile caps grep matches per individual file.
	maxMatchesPerFile = 50

	// maxGlobResults is the maximum number of glob matches returned.
	maxGlobResults = 100

	// maxRgStatEntries caps the number of files we stat for mod-time sorting.
	maxRgStatEntries = 1000

	// maxListEntries is the maximum number of directory entries returned.
	maxListEntries = 500
)

// --- Background job limits ---

const (
	// MaxBackgroundJobs is the maximum number of concurrent background jobs.
	MaxBackgroundJobs = 50

	// completedJobRetention is how long completed jobs are kept before cleanup.
	completedJobRetention = 8 * time.Hour
)

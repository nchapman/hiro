package tools

import "time"

// Resource limit constants for agent tools, organized by category.

// --- Timeouts ---

const (
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

	// outputPollInterval is the polling interval for bash job output checks.
	outputPollInterval = 100 * time.Millisecond

	// dialTimeout is the timeout for establishing network connections.
	dialTimeout = 10 * time.Second
)

// --- Output and buffer sizes ---

const (
	// maxOutputLen is the maximum length of synchronous bash output (bytes).
	maxOutputLen = 32000

	// outputTruncateHalf is half of maxOutputLen, used when truncating output
	// to keep the beginning and end.
	outputTruncateHalf = maxOutputLen / 2

	// maxBufferBytes is the maximum size of each stdout/stderr buffer (4MB).
	maxBufferBytes = 4 << 20

	// maxResponseBody is the maximum size of an HTTP fetch response (bytes).
	maxResponseBody = 64000

	// maxFileReadLen is the maximum output length when reading a file (bytes).
	maxFileReadLen = 64000

	// binaryDetectBufSize is the number of bytes read to detect binary files.
	binaryDetectBufSize = 512

	// scannerInitBufSize is the initial buffer size for line scanners (64KB).
	scannerInitBufSize = 64 * 1024

	// scannerMaxBufSize is the maximum buffer size for line scanners (1MB).
	scannerMaxBufSize = 1024 * 1024
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
)

// --- File permissions ---

const (
	// filePermDefault is the default permission for newly created files.
	filePermDefault = 0o666
)

// --- String splitting ---

const (
	// splitKeyValueParts is the expected number of parts when splitting key:value strings.
	splitKeyValueParts = 2
)

// --- Background job limits ---

const (
	// MaxBackgroundJobs is the maximum number of concurrent background jobs.
	MaxBackgroundJobs = 50

	// completedJobRetention is how long completed jobs are kept before cleanup.
	completedJobRetention = 8 * time.Hour
)

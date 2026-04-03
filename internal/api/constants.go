package api

import "time"

// Cluster role constants.
const (
	roleStandalone = "standalone"
	roleLeader     = "leader"
	roleWorker     = "worker"
)

// Shared constants used across multiple files in the api package.
const (
	// minPasswordLength is the minimum password length for setup and change-password.
	minPasswordLength = 8

	// defaultTermCols is the default terminal width when the client doesn't specify.
	defaultTermCols = 80

	// defaultTermRows is the default terminal height when the client doesn't specify.
	defaultTermRows = 24

	// restartDelay is the short delay before requesting a process restart,
	// allowing the HTTP response to be written first.
	restartDelay = 100 * time.Millisecond
)

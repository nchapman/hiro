package cluster

import (
	"os"
	"time"
)

// Cluster-specific permission constants.
const (
	// filePermNoExec is a permission mask that strips execute, setuid, setgid,
	// and sticky bits from remote file modes. Specific to the file sync
	// security model.
	filePermNoExec = os.FileMode(0o666)
)

// Discovery constants.
const (
	// discoveryHTTPTimeout is the HTTP client timeout for tracker requests.
	discoveryHTTPTimeout = 10 * time.Second

	// discoveryErrorBodyLimit caps the response body read on tracker errors.
	discoveryErrorBodyLimit = 1024

	// discoveryNodeIDDisplayLen is the max display length for node IDs in results.
	discoveryNodeIDDisplayLen = 12
)

// Relay timing constants.
const (
	// relayDialTimeout is the TCP dial timeout for relay connections.
	relayDialTimeout = 10 * time.Second

	// relayKeepaliveInterval is how often the leader pings the relay to
	// prevent NAT/proxy idle timeouts.
	relayKeepaliveInterval = 15 * time.Second

	// relayWriteDeadline is the write deadline for relay handshakes and pings.
	relayWriteDeadline = 5 * time.Second

	// relayStatusReadDeadline is the read deadline for relay status responses.
	relayStatusReadDeadline = 30 * time.Second

	// relayBackoffInitial is the starting backoff after a relay disconnect.
	relayBackoffInitial = 5 * time.Second

	// relayBackoffMax is the maximum backoff between relay reconnect attempts.
	relayBackoffMax = 120 * time.Second

	// relaySelfTestTimeout is the dial timeout for reachability self-tests.
	relaySelfTestTimeout = 3 * time.Second

	// relayListenerBuffer is the channel buffer size for the ChannelListener.
	relayListenerBuffer = 16
)

// Node bridge constants.
const (
	// readyBufSize is the buffer size for reading the worker ready signal.
	readyBufSize = 64
)

// Stream validation constants.
const (
	// maxNodeNameLen is the maximum length for node names in registration.
	maxNodeNameLen = 128

	// maxNodeIDDisplayLen is the truncation length for node IDs in log messages.
	maxNodeIDDisplayLen = 16
)

// Token generation constants.
const (
	// byteRange is the full range of a single byte, used to calculate
	// rejection sampling thresholds for unbiased random character selection.
	byteRange = 256

	// swarmCodeLen is the number of random characters in a generated swarm code.
	swarmCodeLen = 8
)

// Terminal management constants.
const (
	// termOutputBufSize is the read buffer size for PTY output (32KB).
	termOutputBufSize = 32 * 1024

	// termKillGracePeriod is how long to wait for a terminal process to exit
	// after SIGHUP before sending SIGKILL.
	termKillGracePeriod = 3 * time.Second
)

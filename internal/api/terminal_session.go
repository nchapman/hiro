package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// sessionEvent is sent through subscriber channels. Either data or exit is set.
type sessionEvent struct {
	data     []byte // PTY output (nil for exit event)
	exited   bool
	exitCode int
}

// TerminalSession represents a persistent terminal PTY session that survives
// WebSocket disconnects. The session keeps running until explicitly closed or
// idle-cleaned.
type TerminalSession struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"node_id"`
	CreatedAt time.Time `json:"created_at"`

	// mu protects mutable fields accessed from multiple goroutines.
	mu       sync.Mutex
	lastUsed time.Time
	exited   bool
	exitCode int
	cols     uint16
	rows     uint16

	// Local PTY fields (nil for remote sessions).
	cmd      *exec.Cmd
	ptmx     *os.File
	waitDone chan struct{}

	// Replay buffer for reconnection.
	replay *replayBuffer

	// Subscriber fan-out for live output delivery.
	subsMu sync.Mutex
	subs   map[string]chan sessionEvent
}

// subscribe registers a subscriber and returns its ID and output channel.
// Caller may hold sess.mu — this method only acquires subsMu.
func (s *TerminalSession) subscribe() (string, <-chan sessionEvent) {
	id := generateSessionID()[:8]
	ch := make(chan sessionEvent, subscriberBufferSize)
	s.subsMu.Lock()
	s.subs[id] = ch
	s.subsMu.Unlock()
	return id, ch
}

// unsubscribe removes a subscriber.
func (s *TerminalSession) unsubscribe(id string) {
	s.subsMu.Lock()
	if ch, ok := s.subs[id]; ok {
		close(ch)
		delete(s.subs, id)
	}
	s.subsMu.Unlock()
}

// fanOutData sends PTY output to all subscribers (non-blocking).
func (s *TerminalSession) fanOutData(data []byte) {
	cp := make([]byte, len(data))
	copy(cp, data)
	evt := sessionEvent{data: cp}
	s.subsMu.Lock()
	for _, ch := range s.subs {
		select {
		case ch <- evt:
		default:
		}
	}
	s.subsMu.Unlock()
}

// fanOutExit sends an exit event to all subscribers (non-blocking).
func (s *TerminalSession) fanOutExit(code int) {
	evt := sessionEvent{exited: true, exitCode: code}
	s.subsMu.Lock()
	for _, ch := range s.subs {
		select {
		case ch <- evt:
		default:
		}
	}
	s.subsMu.Unlock()
}

// hasSubscribers reports whether any subscribers are attached.
func (s *TerminalSession) hasSubscribers() bool {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	return len(s.subs) > 0
}

// snapshot reads mutable fields under lock.
func (s *TerminalSession) snapshot() (exited bool, exitCode int, lastUsed time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exited, s.exitCode, s.lastUsed
}

// RemoteTerminalSender sends terminal-related gRPC messages to worker nodes.
type RemoteTerminalSender interface {
	SendCreateTerminal(nodeID string, sessionID string, cols, rows uint32) error
	SendTerminalInput(nodeID string, sessionID string, data []byte) error
	SendTerminalResize(nodeID string, sessionID string, cols, rows uint32) error
	SendCloseTerminal(nodeID string, sessionID string) error
}

// TerminalSessionManager owns all terminal sessions. Sessions persist across
// WebSocket disconnects and are only destroyed on explicit close or idle timeout.
type TerminalSessionManager struct {
	mu          sync.Mutex
	sessions    map[string]*TerminalSession
	rootDir     string
	logger      *slog.Logger
	stopOnce    sync.Once
	stopCh      chan struct{}
	remote      RemoteTerminalSender   // nil when clustering is not active
	nodeChecker NodeChecker            // nil when clustering is not active
	createChans map[string]chan string // sessionID -> error string (empty = success)
}

// maxSessionsPerNode limits concurrent terminal sessions per node.
const maxSessionsPerNode = 5

// maxTotalSessions limits the total number of terminal sessions across all nodes.
const maxTotalSessions = 20

// maxReplayBytes is the replay buffer capacity per session.
const maxReplayBytes = 100 * 1024

// idleSessionTimeout is how long an unattached session lives before cleanup.
const idleSessionTimeout = 24 * time.Hour

// subscriberBufferSize is the channel buffer for per-subscriber event delivery.
const subscriberBufferSize = 256

// remoteCreateTimeout is how long to wait for a remote terminal creation response.
const remoteCreateTimeout = 10 * time.Second

// ptyReadBufferSize is the buffer size for reading PTY output.
const ptyReadBufferSize = 32 * 1024

// gracefulShutdownTimeout is how long to wait after SIGHUP before sending SIGKILL.
const gracefulShutdownTimeout = 3 * time.Second

// idleCleanupInterval is how often the cleanup loop checks for idle sessions.
const idleCleanupInterval = 10 * time.Minute

// sessionIDRandomBytes is the number of random bytes used to generate session IDs.
const sessionIDRandomBytes = 16

// NewTerminalSessionManager creates a new session manager and starts the
// idle cleanup goroutine.
func NewTerminalSessionManager(rootDir string, logger *slog.Logger) *TerminalSessionManager {
	m := &TerminalSessionManager{
		sessions:    make(map[string]*TerminalSession),
		createChans: make(map[string]chan string),
		rootDir:     rootDir,
		logger:      logger.With("component", "terminal-sessions"),
		stopCh:      make(chan struct{}),
	}
	go m.cleanupLoop()
	return m
}

// SetRemote configures the remote terminal sender for cluster mode.
func (m *TerminalSessionManager) SetRemote(remote RemoteTerminalSender) {
	m.mu.Lock()
	m.remote = remote
	m.mu.Unlock()
}

// NodeChecker validates that a node ID is approved and online.
type NodeChecker interface {
	IsOnlineApproved(nodeID string) bool
}

// SetNodeChecker configures the node checker for validating remote node IDs.
func (m *TerminalSessionManager) SetNodeChecker(nc NodeChecker) {
	m.mu.Lock()
	m.nodeChecker = nc
	m.mu.Unlock()
}

// checkLimits verifies that creating a new session on nodeID won't exceed
// per-node or global caps. Caller must hold m.mu.
func (m *TerminalSessionManager) checkLimits(nodeID string) error {
	if len(m.sessions) >= maxTotalSessions {
		return fmt.Errorf("too many total terminal sessions (max %d)", maxTotalSessions)
	}
	count := 0
	for _, s := range m.sessions {
		ex, _, _ := s.snapshot()
		if s.NodeID == nodeID && !ex {
			count++
		}
	}
	if count >= maxSessionsPerNode {
		return fmt.Errorf("too many terminal sessions on node %q (max %d)", nodeID, maxSessionsPerNode)
	}
	return nil
}

// HandleTerminalCreated processes a TerminalCreated response from a worker.
func (m *TerminalSessionManager) HandleTerminalCreated(nodeID, sessionID, errMsg string) {
	m.mu.Lock()
	ch, ok := m.createChans[sessionID]
	if ok {
		delete(m.createChans, sessionID)
	}
	m.mu.Unlock()
	if ok {
		ch <- errMsg
	}
}

// HandleTerminalOutput processes PTY output from a remote worker.
func (m *TerminalSessionManager) HandleTerminalOutput(nodeID, sessionID string, data []byte) {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	m.mu.Unlock()
	if !ok {
		return
	}
	sess.replay.Write(data)
	sess.fanOutData(data)
	sess.mu.Lock()
	sess.lastUsed = time.Now()
	sess.mu.Unlock()
}

// HandleTerminalExited processes a shell exit from a remote worker.
func (m *TerminalSessionManager) HandleTerminalExited(nodeID, sessionID string, exitCode int) {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	m.mu.Unlock()
	if !ok {
		return
	}
	sess.mu.Lock()
	sess.exited = true
	sess.exitCode = exitCode
	sess.mu.Unlock()
	sess.fanOutExit(exitCode)
	m.logger.Info("remote terminal exited", "id", sessionID, "node", nodeID, "code", exitCode)
}

// Create spawns a new terminal session (local or remote). Returns the session
// or an error if the per-node limit is reached.
func (m *TerminalSessionManager) Create(nodeID string, cols, rows uint16) (*TerminalSession, error) {
	if nodeID == "" || nodeID == nodeIDHome {
		nodeID = nodeIDHome
	}

	if nodeID == nodeIDHome {
		return m.createLocal(nodeID, cols, rows)
	}

	return m.createRemote(nodeID, cols, rows)
}

// createLocal spawns a local PTY session. Caller must pass a validated nodeID.
func (m *TerminalSessionManager) createLocal(nodeID string, cols, rows uint16) (*TerminalSession, error) {
	cols, rows = applyDefaultSize(cols, rows)

	// Check limits before spawning the PTY to avoid wasted process creation.
	m.mu.Lock()
	if err := m.checkLimits(nodeID); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.mu.Unlock()

	cmd, ptmx, err := m.spawnPTY(cols, rows)
	if err != nil {
		return nil, err
	}

	sess := newLocalSession(nodeID, cmd, ptmx, cols, rows)

	// Reap the process.
	go func() {
		_ = cmd.Wait()
		close(sess.waitDone)
	}()

	// Re-check limits and insert atomically. A concurrent create could have
	// slipped in between our pre-check and the PTY spawn.
	m.mu.Lock()
	if err := m.checkLimits(nodeID); err != nil {
		m.mu.Unlock()
		_ = cmd.Process.Kill()
		<-sess.waitDone
		ptmx.Close()
		return nil, err
	}
	m.sessions[sess.ID] = sess
	m.mu.Unlock()

	// Start output pump only after the session is in the map.
	go m.outputPump(sess)

	m.logger.Info("terminal session created", "id", sess.ID, "node", sess.NodeID)
	return sess, nil
}

// spawnPTY starts a shell process with a PTY of the given size.
func (m *TerminalSessionManager) spawnPTY(cols, rows uint16) (*exec.Cmd, *os.File, error) {
	shell := "/bin/bash"
	if _, err := exec.LookPath(shell); err != nil {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell)
	cmd.Dir = m.rootDir
	cmd.Env = terminalEnvForSession(m.rootDir)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, nil, fmt.Errorf("start pty: %w", err)
	}
	return cmd, ptmx, nil
}

// newLocalSession creates a TerminalSession for a local PTY.
func newLocalSession(nodeID string, cmd *exec.Cmd, ptmx *os.File, cols, rows uint16) *TerminalSession {
	now := time.Now()
	return &TerminalSession{
		ID:        generateSessionID(),
		NodeID:    nodeID,
		CreatedAt: now,
		lastUsed:  now,
		cmd:       cmd,
		ptmx:      ptmx,
		waitDone:  make(chan struct{}),
		replay:    newReplayBuffer(maxReplayBytes),
		subs:      make(map[string]chan sessionEvent),
		cols:      cols,
		rows:      rows,
	}
}

// applyDefaultSize returns cols and rows with defaults applied for zero values.
func applyDefaultSize(cols, rows uint16) (uint16, uint16) {
	if cols == 0 {
		cols = defaultTermCols
	}
	if rows == 0 {
		rows = defaultTermRows
	}
	return cols, rows
}

// createRemote creates a terminal session on a remote worker node via gRPC.
func (m *TerminalSessionManager) createRemote(nodeID string, cols, rows uint16) (*TerminalSession, error) {
	m.mu.Lock()
	remote := m.remote
	nc := m.nodeChecker
	m.mu.Unlock()

	if remote == nil {
		return nil, fmt.Errorf("cluster not configured — cannot create remote terminal")
	}

	// Validate the node is approved and online before creating a session.
	if nc != nil && !nc.IsOnlineApproved(nodeID) {
		return nil, fmt.Errorf("node %q is not available", nodeID)
	}

	cols, rows = applyDefaultSize(cols, rows)
	sess := newRemoteSession(nodeID, cols, rows)

	// Register a channel to receive the create response.
	createCh := make(chan string, 1)
	m.mu.Lock()
	if err := m.checkLimits(nodeID); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.createChans[sess.ID] = createCh
	m.sessions[sess.ID] = sess
	m.mu.Unlock()

	if err := m.sendAndAwaitRemoteCreate(remote, nodeID, sess, cols, rows, createCh); err != nil {
		return nil, err
	}

	m.logger.Info("remote terminal session created", "id", sess.ID, "node", nodeID)
	return sess, nil
}

// newRemoteSession creates a TerminalSession for a remote node (no local PTY).
func newRemoteSession(nodeID string, cols, rows uint16) *TerminalSession {
	now := time.Now()
	return &TerminalSession{
		ID:        generateSessionID(),
		NodeID:    nodeID,
		CreatedAt: now,
		lastUsed:  now,
		replay:    newReplayBuffer(maxReplayBytes),
		subs:      make(map[string]chan sessionEvent),
		cols:      cols,
		rows:      rows,
	}
}

// sendAndAwaitRemoteCreate sends the create terminal request and waits for
// the worker's response. On failure it cleans up the session from the map.
func (m *TerminalSessionManager) sendAndAwaitRemoteCreate(remote RemoteTerminalSender, nodeID string, sess *TerminalSession, cols, rows uint16, createCh chan string) error {
	// Send create request to the worker.
	if err := remote.SendCreateTerminal(nodeID, sess.ID, uint32(cols), uint32(rows)); err != nil {
		m.mu.Lock()
		delete(m.createChans, sess.ID)
		delete(m.sessions, sess.ID)
		m.mu.Unlock()
		return fmt.Errorf("send create terminal: %w", err)
	}

	// Wait for response with timeout.
	select {
	case errMsg := <-createCh:
		if errMsg != "" {
			m.mu.Lock()
			delete(m.sessions, sess.ID)
			m.mu.Unlock()
			return fmt.Errorf("remote terminal creation failed: %s", errMsg)
		}
	case <-time.After(remoteCreateTimeout):
		m.mu.Lock()
		delete(m.createChans, sess.ID)
		delete(m.sessions, sess.ID)
		m.mu.Unlock()
		// Clean up the orphaned PTY on the worker.
		_ = remote.SendCloseTerminal(nodeID, sess.ID)
		return fmt.Errorf("timeout waiting for remote terminal creation")
	}
	return nil
}

// outputPump reads PTY output and writes to the replay buffer and subscribers.
// The exit sentinel is never written to the replay buffer — it is delivered
// only via the typed sessionEvent channel to avoid polluting replay data.
func (m *TerminalSessionManager) outputPump(sess *TerminalSession) {
	buf := make([]byte, ptyReadBufferSize)
	for {
		n, err := sess.ptmx.Read(buf)
		if n > 0 {
			sess.replay.Write(buf[:n])
			sess.fanOutData(buf[:n])
			sess.mu.Lock()
			sess.lastUsed = time.Now()
			sess.mu.Unlock()
		}
		if err != nil {
			// PTY closed — shell exited.
			<-sess.waitDone
			code := 0
			if sess.cmd.ProcessState != nil {
				code = sess.cmd.ProcessState.ExitCode()
			}

			sess.mu.Lock()
			sess.exited = true
			sess.exitCode = code
			sess.mu.Unlock()

			sess.fanOutExit(code)
			m.logger.Info("terminal session exited", "id", sess.ID, "code", code)
			return
		}
	}
}

// Attach registers a subscriber to a session and returns the subscriber ID,
// output channel, and replay data. Subscribe + replay snapshot + exit state
// are all read under sess.mu to prevent data loss between replay and live.
func (m *TerminalSessionManager) Attach(sessionID string) (subID string, ch <-chan sessionEvent, replayData []byte, exited bool, exitCode int, err error) { //nolint:gocritic // complex return is intentional for this internal API
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	m.mu.Unlock()
	if !ok {
		return "", nil, nil, false, 0, fmt.Errorf("session not found: %s", sessionID)
	}

	// Subscribe first under sess.mu so no output is lost between the replay
	// snapshot and the live channel. subscribe() only acquires subsMu, so
	// holding sess.mu here is safe (lock order: mu -> subsMu).
	sess.mu.Lock()
	subID, ch = sess.subscribe()
	replayData = sess.replay.Bytes()
	exited = sess.exited
	exitCode = sess.exitCode
	sess.lastUsed = time.Now()
	sess.mu.Unlock()

	return subID, ch, replayData, exited, exitCode, nil
}

// Detach removes a subscriber from a session without killing the PTY.
func (m *TerminalSessionManager) Detach(sessionID, subID string) {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	m.mu.Unlock()
	if !ok {
		return
	}
	sess.unsubscribe(subID)
}

// WriteInput writes user input to a session's PTY.
func (m *TerminalSessionManager) WriteInput(sessionID string, data []byte) error {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	remote := m.remote
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	sess.mu.Lock()
	if sess.exited {
		sess.mu.Unlock()
		return fmt.Errorf("session has exited")
	}
	sess.lastUsed = time.Now()
	sess.mu.Unlock()

	// Remote session: forward via gRPC.
	if sess.ptmx == nil && remote != nil {
		return remote.SendTerminalInput(sess.NodeID, sessionID, data)
	}
	_, err := sess.ptmx.Write(data)
	return err
}

// Resize changes the PTY window size for a session.
func (m *TerminalSessionManager) Resize(sessionID string, cols, rows uint16) error {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	remote := m.remote
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	sess.mu.Lock()
	if sess.exited {
		sess.mu.Unlock()
		return nil
	}
	sess.cols = cols
	sess.rows = rows
	sess.lastUsed = time.Now()
	sess.mu.Unlock()

	// Remote session: forward via gRPC.
	if sess.ptmx == nil && remote != nil {
		return remote.SendTerminalResize(sess.NodeID, sessionID, uint32(cols), uint32(rows))
	}
	return pty.Setsize(sess.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}

// Close destroys a session, killing the PTY process.
func (m *TerminalSessionManager) Close(sessionID string) error {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session not found: %s", sessionID)
	}
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	m.killSession(sess)
	m.logger.Info("terminal session closed", "id", sessionID)
	return nil
}

// killSession terminates a session's PTY process (local) or sends a close
// command (remote).
func (m *TerminalSessionManager) killSession(sess *TerminalSession) {
	// Close all subscribers.
	sess.subsMu.Lock()
	for id, ch := range sess.subs {
		close(ch)
		delete(sess.subs, id)
	}
	sess.subsMu.Unlock()

	sess.mu.Lock()
	exited := sess.exited
	sess.mu.Unlock()

	if exited {
		return
	}

	// Remote session: send close via gRPC.
	if sess.cmd == nil {
		m.mu.Lock()
		remote := m.remote
		m.mu.Unlock()
		if remote != nil {
			_ = remote.SendCloseTerminal(sess.NodeID, sess.ID)
		}
		return
	}

	// Local session: graceful shutdown: SIGHUP -> wait 3s -> SIGKILL.
	select {
	case <-sess.waitDone:
		return
	default:
	}

	_ = sess.cmd.Process.Signal(syscall.SIGHUP)
	select {
	case <-sess.waitDone:
	case <-time.After(gracefulShutdownTimeout):
		_ = sess.cmd.Process.Kill()
		<-sess.waitDone
	}
	sess.ptmx.Close()
}

// List returns metadata for all sessions.
func (m *TerminalSessionManager) List() []TerminalSessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]TerminalSessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		ex, code, _ := s.snapshot()
		list = append(list, TerminalSessionInfo{
			ID:        s.ID,
			NodeID:    s.NodeID,
			CreatedAt: s.CreatedAt,
			Exited:    ex,
			ExitCode:  code,
		})
	}
	return list
}

// TerminalSessionInfo is the JSON-serializable metadata for a session.
type TerminalSessionInfo struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"node_id"`
	CreatedAt time.Time `json:"created_at"`
	Exited    bool      `json:"exited"`
	ExitCode  int       `json:"exit_code,omitempty"`
}

// Shutdown kills all sessions. Called on server shutdown.
func (m *TerminalSessionManager) Shutdown() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})

	m.mu.Lock()
	sessions := make([]*TerminalSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = make(map[string]*TerminalSession)
	m.mu.Unlock()

	for _, s := range sessions {
		m.killSession(s)
	}
}

// cleanupLoop periodically removes idle sessions.
func (m *TerminalSessionManager) cleanupLoop() {
	ticker := time.NewTicker(idleCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.cleanupIdle()
		}
	}
}

// cleanupIdle removes sessions that have been idle for longer than the timeout.
func (m *TerminalSessionManager) cleanupIdle() {
	m.mu.Lock()
	var toClose []string
	for id, s := range m.sessions {
		_, _, lastUsed := s.snapshot()
		if !s.hasSubscribers() && time.Since(lastUsed) > idleSessionTimeout {
			toClose = append(toClose, id)
		}
	}
	m.mu.Unlock()

	for _, id := range toClose {
		m.logger.Info("cleaning up idle terminal session", "id", id)
		m.Close(id)
	}
}

// --- Control message types for the multiplexed WebSocket protocol ---

// termControlMsg is a JSON control message sent/received over the multiplexed
// WebSocket (message type 0x03).
type termControlMsg struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
	NodeName  string `json:"node_name,omitempty"`
	Cols      uint16 `json:"cols,omitempty"`
	Rows      uint16 `json:"rows,omitempty"`
	Code      *int   `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
}

func marshalControl(sessionID string, msg termControlMsg) []byte {
	payload, _ := json.Marshal(msg)
	frame := make([]byte, 1+sessionIDLen+len(payload))
	frame[0] = termMsgControl
	copy(frame[1:1+sessionIDLen], padSessionID(sessionID))
	copy(frame[1+sessionIDLen:], payload)
	return frame
}

func marshalOutput(sessionID string, data []byte) []byte {
	frame := make([]byte, 1+sessionIDLen+len(data))
	frame[0] = termMsgOutput
	copy(frame[1:1+sessionIDLen], padSessionID(sessionID))
	copy(frame[1+sessionIDLen:], data)
	return frame
}

func padSessionID(id string) []byte {
	b := make([]byte, sessionIDLen)
	copy(b, id)
	return b
}

// --- Helpers ---

// generateSessionID creates a cryptographically random 32-char hex string.
func generateSessionID() string {
	b := make([]byte, sessionIDRandomBytes)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// --- Replay buffer ---

// replayBuffer is a circular byte buffer that captures PTY output for replay.
// Capacity is tracked explicitly because Go's append may reallocate, making
// cap(buf) unreliable as a limit.
type replayBuffer struct {
	mu       sync.Mutex
	buf      []byte
	capacity int
}

func newReplayBuffer(capacity int) *replayBuffer {
	return &replayBuffer{
		buf:      make([]byte, 0, capacity),
		capacity: capacity,
	}
}

// Write appends data to the buffer. If the buffer exceeds capacity, the oldest
// bytes are discarded.
func (r *replayBuffer) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.buf = append(r.buf, p...)
	if len(r.buf) > r.capacity {
		excess := len(r.buf) - r.capacity
		r.buf = r.buf[excess:]
	}
}

// Bytes returns a copy of the buffered data, oldest to newest.
func (r *replayBuffer) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]byte, len(r.buf))
	copy(cp, r.buf)
	return cp
}

// --- Environment ---

// terminalEnvForSession builds the environment for a terminal session.
func terminalEnvForSession(_ string) []string {
	env := []string{
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
	}
	for _, key := range []string{"PATH", "HOME", "USER", "SHELL", "EDITOR",
		"MISE_DATA_DIR", "MISE_CONFIG_DIR", "MISE_CACHE_DIR",
		"MISE_GLOBAL_CONFIG_FILE", "MISE_INSTALL_PATH",
		"STARSHIP_CONFIG"} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	return env
}

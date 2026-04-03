package cluster

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
)

// WorkerTerminalManager handles terminal sessions on a worker node.
// It spawns local PTY processes and relays I/O to the leader via the
// WorkerStream.
type WorkerTerminalManager struct {
	stream  *WorkerStream
	rootDir string
	logger  *slog.Logger

	mu       sync.Mutex
	sessions map[string]*workerTermSession
}

type workerTermSession struct {
	id       string
	cmd      *exec.Cmd
	ptmx     *os.File
	waitDone chan struct{}
	exitCode int
	closed   bool // set by handleClose to prevent spurious exit from outputPump
}

// NewWorkerTerminalManager creates a new worker terminal manager and wires
// up the handler callbacks on the WorkerStream.
func NewWorkerTerminalManager(stream *WorkerStream, rootDir string, logger *slog.Logger) *WorkerTerminalManager {
	m := &WorkerTerminalManager{
		stream:   stream,
		rootDir:  rootDir,
		logger:   logger.With("component", "worker-terminal"),
		sessions: make(map[string]*workerTermSession),
	}

	stream.SetCreateTerminalHandler(m.handleCreate)
	stream.SetTerminalInputHandler(m.handleInput)
	stream.SetTerminalResizeHandler(m.handleResize)
	stream.SetCloseTerminalHandler(m.handleClose)

	return m
}

func (m *WorkerTerminalManager) handleCreate(_ context.Context, msg *pb.CreateTerminal) {
	shell := "/bin/bash"
	if _, err := exec.LookPath(shell); err != nil {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell)
	cmd.Dir = m.rootDir
	cmd.Env = workerTerminalEnv()

	cols := uint16(msg.Cols) //nolint:gosec // terminal dimensions are small values
	rows := uint16(msg.Rows) //nolint:gosec // terminal dimensions are small values
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		_ = m.stream.SendTerminalCreated(msg.SessionId, err.Error())
		return
	}

	sess := &workerTermSession{
		id:       msg.SessionId,
		cmd:      cmd,
		ptmx:     ptmx,
		waitDone: make(chan struct{}),
	}

	// Reap process.
	go func() {
		_ = cmd.Wait()
		if cmd.ProcessState != nil {
			sess.exitCode = cmd.ProcessState.ExitCode()
		}
		close(sess.waitDone)
	}()

	m.mu.Lock()
	m.sessions[msg.SessionId] = sess
	m.mu.Unlock()

	// Notify leader of success.
	_ = m.stream.SendTerminalCreated(msg.SessionId, "")

	// Output pump: PTY -> leader.
	go m.outputPump(sess)

	m.logger.Info("terminal created", "session_id", msg.SessionId)
}

func (m *WorkerTerminalManager) outputPump(sess *workerTermSession) {
	buf := make([]byte, termOutputBufSize)
	for {
		n, err := sess.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			_ = m.stream.SendTerminalOutput(sess.id, data)
		}
		if err != nil {
			<-sess.waitDone

			// If handleClose already removed this session, don't send a
			// spurious exit event or double-delete from the map.
			m.mu.Lock()
			_, stillPresent := m.sessions[sess.id]
			if stillPresent {
				delete(m.sessions, sess.id)
			}
			closed := sess.closed
			m.mu.Unlock()

			if !closed {
				_ = m.stream.SendTerminalExited(sess.id, int32(sess.exitCode)) //nolint:gosec // exit codes fit int32
				m.logger.Info("terminal exited", "session_id", sess.id, "code", sess.exitCode)
			}
			return
		}
	}
}

func (m *WorkerTerminalManager) handleInput(_ context.Context, msg *pb.TerminalInput) {
	m.mu.Lock()
	sess, ok := m.sessions[msg.SessionId]
	m.mu.Unlock()
	if !ok {
		return
	}
	_, _ = sess.ptmx.Write(msg.Data)
}

func (m *WorkerTerminalManager) handleResize(_ context.Context, msg *pb.TerminalResize) {
	m.mu.Lock()
	sess, ok := m.sessions[msg.SessionId]
	m.mu.Unlock()
	if !ok {
		return
	}
	_ = pty.Setsize(sess.ptmx, &pty.Winsize{
		Rows: uint16(msg.Rows), //nolint:gosec // terminal dimensions are small values
		Cols: uint16(msg.Cols), //nolint:gosec // terminal dimensions are small values
	})
}

func (m *WorkerTerminalManager) handleClose(_ context.Context, msg *pb.CloseTerminal) {
	m.mu.Lock()
	sess, ok := m.sessions[msg.SessionId]
	if ok {
		sess.closed = true // signal outputPump not to send spurious exit
		delete(m.sessions, msg.SessionId)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	m.killSession(sess)
	m.logger.Info("terminal closed by leader", "session_id", msg.SessionId)
}

func (m *WorkerTerminalManager) killSession(sess *workerTermSession) {
	select {
	case <-sess.waitDone:
		return
	default:
	}

	_ = sess.cmd.Process.Signal(syscall.SIGHUP)
	select {
	case <-sess.waitDone:
	case <-time.After(termKillGracePeriod):
		_ = sess.cmd.Process.Kill()
		<-sess.waitDone
	}
	sess.ptmx.Close()
}

// Shutdown kills all active terminal sessions on this worker.
func (m *WorkerTerminalManager) Shutdown() {
	m.mu.Lock()
	sessions := make([]*workerTermSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = make(map[string]*workerTermSession)
	m.mu.Unlock()

	for _, s := range sessions {
		m.killSession(s)
	}
}

// workerTerminalEnv builds a minimal environment for terminal sessions on workers.
func workerTerminalEnv() []string {
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

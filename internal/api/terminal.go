package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// maxTerminalSessions limits concurrent terminal connections.
const maxTerminalSessions = 5

// activeTerminals tracks the number of active terminal sessions.
var activeTerminals atomic.Int32

// handleTerminal upgrades to a WebSocket and spawns an interactive PTY session.
// The client sends raw keystrokes as binary frames and resize commands as JSON
// text frames. The server streams PTY output back as binary frames.
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	// Terminal requires setup to be complete — auth is enforced by requireAuth middleware.
	if s.cp != nil && s.cp.NeedsSetup() {
		http.Error(w, "unavailable during setup", http.StatusServiceUnavailable)
		return
	}

	// Enforce concurrent session limit (atomic add-then-check to avoid TOCTOU race).
	if activeTerminals.Add(1) > int32(maxTerminalSessions) {
		activeTerminals.Add(-1)
		http.Error(w, "too many terminal sessions", http.StatusServiceUnavailable)
		return
	}
	defer activeTerminals.Add(-1)

	cols, rows := parseTermSize(r)

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{r.Host},
	})
	if err != nil {
		s.logger.Error("terminal websocket accept failed", "error", err)
		return
	}
	defer conn.CloseNow()

	// Allow large pastes (default 32KB is too small for terminal use).
	conn.SetReadLimit(1 * 1024 * 1024) // 1 MB

	// Find a shell.
	shell := "/bin/bash"
	if _, err := exec.LookPath(shell); err != nil {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell)
	cmd.Dir = s.rootDir

	// Optional working directory relative to platform root.
	if dir := r.URL.Query().Get("dir"); dir != "" {
		absDir := filepath.Join(s.rootDir, filepath.Clean(dir))
		// Ensure the resolved path stays within rootDir.
		if strings.HasPrefix(absDir, s.rootDir+string(filepath.Separator)) || absDir == s.rootDir {
			if info, err := os.Stat(absDir); err == nil && info.IsDir() {
				cmd.Dir = absDir
			}
		}
	}
	cmd.Env = terminalEnv()

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		s.logger.Error("failed to start pty", "error", err)
		conn.Close(websocket.StatusInternalError, "failed to start shell")
		return
	}
	defer ptmx.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Single owner of cmd.Wait — avoids double-call race between
	// the writePump (shell exits) and cleanup (WebSocket closes).
	waitDone := make(chan struct{})
	var exitCode int
	go func() {
		_ = cmd.Wait()
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		close(waitDone)
	}()

	// Signal that the shell is ready.
	_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"started"}`))

	// writePump: PTY → WebSocket.
	// When the shell exits, sends an "exited" control message and cancels the context.
	go func() {
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if writeErr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
				// Shell exited — wait for reap, then notify client.
				<-waitDone
				msg := fmt.Sprintf(`{"type":"exited","code":%d}`, exitCode)
				_ = conn.Write(context.Background(), websocket.MessageText, []byte(msg))
				return
			}
		}
	}()

	// readPump: WebSocket → PTY (binary) or control messages (text).
	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		switch msgType {
		case websocket.MessageBinary:
			_, _ = ptmx.Write(data)
		case websocket.MessageText:
			var ctrl struct {
				Type string `json:"type"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if json.Unmarshal(data, &ctrl) == nil && ctrl.Type == "resize" {
				_ = pty.Setsize(ptmx, &pty.Winsize{Rows: ctrl.Rows, Cols: ctrl.Cols})
			}
		}
	}

	// Cleanup: terminate process if it hasn't exited yet.
	select {
	case <-waitDone:
		// Already exited.
	default:
		_ = cmd.Process.Signal(syscall.SIGHUP)
		select {
		case <-waitDone:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-waitDone
		}
	}
}

// terminalEnv builds an explicit environment for the terminal shell.
// Only essential variables are included — secrets (HIVE_API_KEY, etc.)
// are deliberately excluded to prevent credential exposure.
func terminalEnv() []string {
	env := []string{
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
	}
	// Pass through safe, non-secret variables.
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

// parseTermSize extracts cols and rows from query params, defaulting to 80x24.
func parseTermSize(r *http.Request) (cols, rows uint16) {
	cols, rows = 80, 24
	if v := r.URL.Query().Get("cols"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 16); err == nil && n > 0 {
			cols = uint16(n)
		}
	}
	if v := r.URL.Query().Get("rows"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 16); err == nil && n > 0 {
			rows = uint16(n)
		}
	}
	return
}

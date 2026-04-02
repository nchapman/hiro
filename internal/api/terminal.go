package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/nchapman/hiro/internal/cluster"
)

// Wire protocol constants for the multiplexed terminal WebSocket.
const (
	termMsgOutput  byte = 0x01 // server -> client: PTY output
	termMsgInput   byte = 0x02 // client -> server: keystrokes
	termMsgControl byte = 0x03 // bidirectional: JSON control
)

// sessionIDLen is the fixed width of the session ID field in the binary header.
const sessionIDLen = 32

// handleTerminal upgrades to a WebSocket and serves a multiplexed terminal
// connection. All terminal sessions share this single WebSocket. The binary
// protocol uses a fixed 33-byte header (1 byte type + 32 byte session ID)
// followed by the payload.
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	if s.cp != nil && s.cp.NeedsSetup() {
		http.Error(w, "unavailable during setup", http.StatusServiceUnavailable)
		return
	}

	if s.termSessions == nil {
		http.Error(w, "terminal sessions not available", http.StatusServiceUnavailable)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{r.Host},
	})
	if err != nil {
		s.logger.Error("terminal websocket accept failed", "error", err)
		return
	}
	defer conn.CloseNow()

	// Allow large pastes.
	conn.SetReadLimit(1 * 1024 * 1024)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	ts := &termSocket{
		conn:     conn,
		ctx:      ctx,
		cancel:   cancel,
		server:   s,
		attached: make(map[string]attachedSub),
	}

	// Sync existing sessions to the client.
	ts.syncSessions()

	// If no sessions exist, auto-create one.
	sessions := s.termSessions.List()
	if len(sessions) == 0 {
		ts.handleCreate(termControlMsg{NodeID: "home", Cols: 80, Rows: 24})
	}

	// Read loop: client -> server.
	ts.readLoop()

	// Detach all sessions on disconnect.
	ts.detachAll()
}

// termSocket manages a single multiplexed WebSocket connection.
type termSocket struct {
	conn     *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	server   *Server
	attached map[string]attachedSub // sessionID -> subscriber info
}

type attachedSub struct {
	subID  string
	cancel context.CancelFunc
}

// resolveNodeName looks up the human-readable name for a node ID.
func (ts *termSocket) resolveNodeName(nodeID string) string {
	if nodeID == "home" {
		// Try the registry first for the configured home name.
		if ts.server.nodeRegistry != nil {
			if info, ok := ts.server.nodeRegistry.Get("home"); ok && info.Name != "" {
				return info.Name
			}
		}
		return "local"
	}
	if ts.server.nodeRegistry != nil {
		if info, ok := ts.server.nodeRegistry.Get(cluster.NodeID(nodeID)); ok && info.Name != "" {
			return info.Name
		}
	}
	return nodeID
}

// syncSessions sends the current session list and replay data to the client.
func (ts *termSocket) syncSessions() {
	sessions := ts.server.termSessions.List()
	for _, info := range sessions {
		// Send "created" control message.
		msg := termControlMsg{
			Type:      "created",
			SessionID: info.ID,
			NodeID:    info.NodeID,
			NodeName:  ts.resolveNodeName(info.NodeID),
		}
		_ = ts.conn.Write(ts.ctx, websocket.MessageBinary, marshalControl(info.ID, msg))

		// Attach and send replay.
		ts.attachSession(info.ID)
	}
}

// attachSession subscribes to a session's output and starts a write pump.
func (ts *termSocket) attachSession(sessionID string) {
	subID, ch, replay, exited, exitCode, err := ts.server.termSessions.Attach(sessionID)
	if err != nil {
		errMsg := termControlMsg{Type: "error", Message: err.Error()}
		_ = ts.conn.Write(ts.ctx, websocket.MessageBinary, marshalControl(sessionID, errMsg))
		return
	}

	// Send replay.
	replayStart := termControlMsg{Type: "replay_start"}
	_ = ts.conn.Write(ts.ctx, websocket.MessageBinary, marshalControl(sessionID, replayStart))
	if len(replay) > 0 {
		_ = ts.conn.Write(ts.ctx, websocket.MessageBinary, marshalOutput(sessionID, replay))
	}
	replayEnd := termControlMsg{Type: "replay_end"}
	_ = ts.conn.Write(ts.ctx, websocket.MessageBinary, marshalControl(sessionID, replayEnd))

	// If already exited, send exit message.
	if exited {
		code := exitCode
		exitMsg := termControlMsg{Type: "exited", Code: &code}
		_ = ts.conn.Write(ts.ctx, websocket.MessageBinary, marshalControl(sessionID, exitMsg))
	}

	// Start write pump goroutine for live output.
	pumpCtx, pumpCancel := context.WithCancel(ts.ctx)
	ts.attached[sessionID] = attachedSub{subID: subID, cancel: pumpCancel}

	go ts.writePump(pumpCtx, sessionID, ch)
}

// writePump forwards live output from a session to the WebSocket.
func (ts *termSocket) writePump(ctx context.Context, sessionID string, ch <-chan sessionEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			if evt.exited {
				code := evt.exitCode
				ctrl := termControlMsg{Type: "exited", Code: &code}
				_ = ts.conn.Write(ctx, websocket.MessageBinary, marshalControl(sessionID, ctrl))
				return // session is done; no more events expected
			}
			_ = ts.conn.Write(ctx, websocket.MessageBinary, marshalOutput(sessionID, evt.data))
		}
	}
}

// readLoop processes incoming binary frames from the client.
func (ts *termSocket) readLoop() {
	for {
		_, data, err := ts.conn.Read(ts.ctx)
		if err != nil {
			return
		}
		if len(data) < 1+sessionIDLen {
			continue
		}

		msgType := data[0]
		sessionID := strings.TrimRight(string(data[1:1+sessionIDLen]), "\x00")
		payload := data[1+sessionIDLen:]

		switch msgType {
		case termMsgInput:
			_ = ts.server.termSessions.WriteInput(sessionID, payload)

		case termMsgControl:
			var ctrl termControlMsg
			if json.Unmarshal(payload, &ctrl) != nil {
				continue
			}
			switch ctrl.Type {
			case "create":
				ts.handleCreate(ctrl)
			case "close":
				ts.handleClose(sessionID)
			case "resize":
				_ = ts.server.termSessions.Resize(sessionID, ctrl.Cols, ctrl.Rows)
			}
		}
	}
}

// handleCreate creates a new terminal session and attaches to it.
func (ts *termSocket) handleCreate(ctrl termControlMsg) {
	nodeID := ctrl.NodeID
	if nodeID == "" {
		nodeID = "home"
	}
	cols := ctrl.Cols
	rows := ctrl.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	sess, err := ts.server.termSessions.Create(nodeID, cols, rows)
	if err != nil {
		errMsg := termControlMsg{Type: "error", Message: err.Error()}
		_ = ts.conn.Write(ts.ctx, websocket.MessageBinary, marshalControl("", errMsg))
		return
	}

	// Send "created" to client.
	msg := termControlMsg{
		Type:      "created",
		SessionID: sess.ID,
		NodeID:    sess.NodeID,
		NodeName:  ts.resolveNodeName(sess.NodeID),
	}
	_ = ts.conn.Write(ts.ctx, websocket.MessageBinary, marshalControl(sess.ID, msg))

	// Attach to start streaming.
	ts.attachSession(sess.ID)
}

// handleClose destroys a terminal session.
func (ts *termSocket) handleClose(sessionID string) {
	// Detach first.
	if sub, ok := ts.attached[sessionID]; ok {
		sub.cancel()
		ts.server.termSessions.Detach(sessionID, sub.subID)
		delete(ts.attached, sessionID)
	}
	// Close the session (kills PTY).
	_ = ts.server.termSessions.Close(sessionID)

	// Notify the client.
	msg := termControlMsg{Type: "closed", SessionID: sessionID}
	_ = ts.conn.Write(ts.ctx, websocket.MessageBinary, marshalControl(sessionID, msg))
}

// detachAll unsubscribes from all sessions without killing them.
func (ts *termSocket) detachAll() {
	for sessionID, sub := range ts.attached {
		sub.cancel()
		ts.server.termSessions.Detach(sessionID, sub.subID)
	}
	ts.attached = nil
}

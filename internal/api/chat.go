package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/nchapman/hivebot/internal/agent"
	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/controlplane"
	"github.com/nchapman/hivebot/internal/ipc"
	"github.com/nchapman/hivebot/internal/watcher"
)

// ChatMessage is a message sent or received over the chat WebSocket.
// For text deltas: type="delta", content="..."
// For tool calls: type="tool_call", tool_call_id, tool_name, input
// For tool results: type="tool_result", tool_call_id, content (output), is_error
// For control: type="done"|"error"|"system"|"message"
type ChatMessage struct {
	Type       string     `json:"type"`
	Role       string     `json:"role,omitempty"`
	Content    string     `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`
	Input      string     `json:"input,omitempty"`
	Output     string     `json:"output,omitempty"`
	IsError    bool       `json:"is_error,omitempty"`
	Status     string     `json:"status,omitempty"`
	Usage      *UsageInfo `json:"usage,omitempty"`
}

// SetManager sets the agent manager and leader agent ID for handling chat.
func (s *Server) SetManager(m *agent.Manager, leaderID string) {
	s.manager = m
	s.leaderID = leaderID
}

// SetControlPlane sets the control plane for auth and slash commands.
func (s *Server) SetControlPlane(cp *controlplane.ControlPlane) {
	s.cp = cp
	s.cmdHandler = cp
}

// SetStartManager sets the callback to start the agent manager.
// Used by the setup endpoint to boot the manager after initial config.
func (s *Server) SetStartManager(fn func() error) {
	s.startManager = fn
}

// SetRootDir sets the platform root directory (used as the terminal working dir).
func (s *Server) SetRootDir(dir string) {
	s.rootDir = dir
}

// SetWatcher sets the filesystem watcher for pushing live updates.
func (s *Server) SetWatcher(w *watcher.Watcher) {
	s.watcher = w
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	// Enforce auth on WebSocket upgrade (browser sends cookies automatically).
	if s.cp != nil && !s.cp.NeedsSetup() && !s.isAuthenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if s.manager == nil || s.leaderID == "" {
		http.Error(w, "no agent configured", http.StatusServiceUnavailable)
		return
	}

	// Allow targeting a specific session via query param, default to leader
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		sessionID = s.leaderID
	}
	info, ok := s.manager.GetSession(sessionID)
	if !ok || info.Mode == config.ModeEphemeral {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if info.Status == agent.SessionStatusStopped {
		http.Error(w, "session is stopped", http.StatusConflict)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Restrict to same-origin. In development, Vite proxies WebSocket
		// requests so the origin matches. In production, the embedded UI
		// is served from the same host.
		OriginPatterns: []string{r.Host},
	})
	if err != nil {
		s.logger.Error("chat websocket accept failed", "error", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := r.Context()

	for {
		// Read user message
		var msg ChatMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logger.Debug("chat connection closed", "error", err)
			return
		}

		if msg.Type != "message" || msg.Content == "" {
			continue
		}

		// Intercept slash commands before they reach the agent.
		if s.cmdHandler != nil && strings.HasPrefix(msg.Content, "/") {
			result, err := s.cmdHandler.HandleCommand(msg.Content)
			if err == nil {
				// Recognized command — send result directly, don't forward to agent.
				resp := ChatMessage{Type: "system", Content: result}
				if writeErr := wsjson.Write(ctx, conn, resp); writeErr != nil {
					return
				}
				done := ChatMessage{Type: "done"}
				if writeErr := wsjson.Write(ctx, conn, done); writeErr != nil {
					return
				}
				continue
			}
			// Unrecognized command — fall through to agent as normal message.
		}

		onEvent := func(evt ipc.ChatEvent) error {
			wireMsg := ChatMessage{
				Type:       evt.Type,
				Role:       "assistant",
				Content:    evt.Content,
				ToolCallID: evt.ToolCallID,
				ToolName:   evt.ToolName,
				Input:      evt.Input,
				Output:     evt.Output,
				IsError:    evt.IsError,
				Status:     evt.Status,
			}
			b, err := json.Marshal(wireMsg)
			if err != nil {
				return err
			}
			return conn.Write(ctx, websocket.MessageText, b)
		}

		// Stream response — agent process owns the conversation.
		_, streamErr := s.manager.SendMessage(ctx, sessionID, msg.Content, onEvent)

		if streamErr != nil {
			errMsg := ChatMessage{Type: "error", Content: streamErr.Error()}
			if writeErr := wsjson.Write(ctx, conn, errMsg); writeErr != nil {
				return
			}
			continue
		}

		// Signal end of response with usage data.
		done := ChatMessage{Type: "done", Role: "assistant"}
		done.Usage = s.buildUsageInfo(sessionID)
		if err := wsjson.Write(ctx, conn, done); err != nil {
			return
		}
	}
}

// buildUsageInfo queries the platform DB for session usage and returns it
// as a UsageInfo struct for inclusion in WebSocket messages.
func (s *Server) buildUsageInfo(sessionID string) *UsageInfo {
	if s.pdb == nil {
		return nil
	}

	var model string
	if s.manager != nil {
		if info, ok := s.manager.GetSession(sessionID); ok {
			model = info.Model
		}
	}

	info := s.buildUsageInfoForSession(sessionID, model)
	return &info
}

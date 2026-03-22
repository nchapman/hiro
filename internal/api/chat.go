package api

import (
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/nchapman/hivebot/internal/agent"
	"github.com/nchapman/hivebot/internal/config"
)

// ChatMessage is a message sent or received over the chat WebSocket.
type ChatMessage struct {
	Type    string `json:"type"`           // "message", "delta", "done", "error"
	Role    string `json:"role,omitempty"` // "user" or "assistant"
	Content string `json:"content"`
}

// SetManager sets the agent manager and leader agent ID for handling chat.
func (s *Server) SetManager(m *agent.Manager, leaderID string) {
	s.manager = m
	s.leaderID = leaderID
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if s.manager == nil || s.leaderID == "" {
		http.Error(w, "no agent configured", http.StatusServiceUnavailable)
		return
	}

	// Allow targeting a specific agent via query param, default to leader
	agentID := r.URL.Query().Get("agent_id")
	if agentID == "" {
		agentID = s.leaderID
	}
	info, ok := s.manager.GetAgent(agentID)
	if !ok || info.ParentID != "" {
		http.Error(w, "agent not found", http.StatusNotFound)
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
	defer conn.CloseNow()

	ctx := r.Context()

	// Persistent agents use SendMessage (shared history persisted to DB).
	// Ephemeral agents use StreamChat with a per-connection conversation.
	persistent := info.Mode == config.ModePersistent
	var conv *agent.Conversation
	if !persistent {
		conv = agent.NewConversation()
	}

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

		onDelta := func(text string) error {
			delta := ChatMessage{Type: "delta", Role: "assistant", Content: text}
			b, _ := json.Marshal(delta)
			return conn.Write(ctx, websocket.MessageText, b)
		}

		// Stream response — persistent agents use shared history, ephemeral get per-connection
		var streamErr error
		if persistent {
			_, streamErr = s.manager.SendMessage(ctx, agentID, msg.Content, onDelta)
		} else {
			_, streamErr = s.manager.StreamChat(ctx, agentID, conv, msg.Content, onDelta)
		}

		if streamErr != nil {
			errMsg := ChatMessage{Type: "error", Content: streamErr.Error()}
			if writeErr := wsjson.Write(ctx, conn, errMsg); writeErr != nil {
				return
			}
			continue
		}

		// Signal end of response
		done := ChatMessage{Type: "done", Role: "assistant"}
		if err := wsjson.Write(ctx, conn, done); err != nil {
			return
		}
	}
}

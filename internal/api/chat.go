package api

import (
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/nchapman/hivebot/internal/agent"
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

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.logger.Error("chat websocket accept failed", "error", err)
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()

	// Each WebSocket connection gets its own conversation history
	conv := agent.NewConversation()

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

		// Stream response from the leader agent with per-connection history
		_, err := s.manager.StreamChat(ctx, s.leaderID, conv, msg.Content, func(text string) error {
			delta := ChatMessage{Type: "delta", Role: "assistant", Content: text}
			b, _ := json.Marshal(delta)
			return conn.Write(ctx, websocket.MessageText, b)
		})

		if err != nil {
			errMsg := ChatMessage{Type: "error", Content: err.Error()}
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

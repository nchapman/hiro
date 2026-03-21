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
	Type    string `json:"type"`          // "message", "delta", "done", "error"
	Role    string `json:"role,omitempty"` // "user" or "assistant"
	Content string `json:"content"`
}

// SetAgent sets the coordinator agent for handling chat messages.
func (s *Server) SetAgent(a *agent.Agent) {
	s.agent = a
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if s.agent == nil {
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

		// Stream response from agent — propagate write errors to abort early
		_, err := s.agent.StreamChat(ctx, msg.Content, func(text string) error {
			delta := ChatMessage{Type: "delta", Role: "assistant", Content: text}
			b, _ := json.Marshal(delta)
			return conn.Write(ctx, websocket.MessageText, b)
		})

		if err != nil {
			errMsg := ChatMessage{Type: "error", Content: err.Error()}
			wsjson.Write(ctx, conn, errMsg)
			continue
		}

		// Signal end of response
		done := ChatMessage{Type: "done", Role: "assistant"}
		wsjson.Write(ctx, conn, done)
	}
}

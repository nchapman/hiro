package api

import (
	"net/http"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/nchapman/hiro/internal/agent"
	"github.com/nchapman/hiro/internal/config"
)

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	// Enforce auth on WebSocket upgrade (browser sends cookies automatically).
	if s.cp != nil && !s.cp.NeedsSetup() && !s.isAuthenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	instanceID, errStr := s.resolveChatInstance(r)
	if errStr != "" {
		http.Error(w, errStr, http.StatusServiceUnavailable)
		return
	}
	info, ok := s.manager.GetInstance(instanceID)
	if !ok || info.Mode == config.ModeEphemeral {
		http.Error(w, "instance not found", http.StatusNotFound)
		return
	}
	if info.Status == agent.InstanceStatusStopped {
		http.Error(w, "instance is stopped", http.StatusConflict)
		return
	}

	if s.webChannel == nil {
		http.Error(w, "web channel not initialized", http.StatusServiceUnavailable)
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

	conversationKey := "web:" + uuid.New().String()
	s.logger.Info("chat connected", "instance_id", instanceID, "conversation_key", conversationKey)

	s.webChannel.HandleConn(r.Context(), conn, instanceID, conversationKey)
}

// resolveChatInstance validates the manager is ready and returns the target
// instance ID. Returns a non-empty error string on failure.
func (s *Server) resolveChatInstance(r *http.Request) (string, string) {
	if !s.hasManager() || s.leaderID == "" {
		return "", "no agent configured"
	}
	instanceID := r.URL.Query().Get("instance_id")
	if instanceID == "" {
		instanceID = s.leaderID
	}
	return instanceID, ""
}

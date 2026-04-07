package api

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/nchapman/hiro/internal/channel"
	"github.com/nchapman/hiro/internal/config"
)

// channelSenderJSON is the JSON shape for a channel sender in API responses.
type channelSenderJSON struct {
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	FirstSeen   string `json:"first_seen"`
	LastSeen    string `json:"last_seen"`
	SampleText  string `json:"sample_text,omitempty"`
	InstanceID  string `json:"instance_id,omitempty"`
}

func senderToJSON(s config.ChannelSender, instanceID string) channelSenderJSON {
	return channelSenderJSON{
		Key:         s.Key,
		DisplayName: s.DisplayName,
		Status:      string(s.Status),
		FirstSeen:   s.FirstSeen.Format("2006-01-02T15:04:05Z"),
		LastSeen:    s.LastSeen.Format("2006-01-02T15:04:05Z"),
		SampleText:  s.SampleText,
		InstanceID:  instanceID,
	}
}

// handleListChannelAccess returns all senders for an instance.
// Reads config directly without the per-instance lock — safe because
// SaveInstanceConfig uses atomic rename, so readers always see a complete file.
// GET /api/instances/{id}/channel-access
func (s *Server) handleListChannelAccess(w http.ResponseWriter, r *http.Request) {
	if !s.hasManager() {
		http.Error(w, "no instance manager", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")

	cfg, err := s.manager.GetInstanceConfig(id)
	if err != nil {
		http.Error(w, "instance not found", http.StatusNotFound)
		return
	}

	var senders []channelSenderJSON
	if cfg.Channels != nil {
		for _, sender := range cfg.Channels.Senders {
			senders = append(senders, senderToJSON(sender, id))
		}
	}
	if senders == nil {
		senders = []channelSenderJSON{}
	}
	writeJSON(w, http.StatusOK, senders)
}

// handleApproveChannelSender approves a pending sender.
// POST /api/instances/{id}/channel-access/{senderKey}/approve
func (s *Server) handleApproveChannelSender(w http.ResponseWriter, r *http.Request) {
	if !s.hasManager() || s.accessChecker == nil {
		http.Error(w, "no instance manager", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if !s.validateInstance(w, id) {
		return
	}
	senderKey, err := url.PathUnescape(r.PathValue("senderKey"))
	if err != nil {
		http.Error(w, "invalid sender key", http.StatusBadRequest)
		return
	}

	if err := s.accessChecker.UpdateSenderStatus(id, senderKey, config.ChannelAccessApproved); err != nil {
		s.writeSenderError(w, id, err)
		return
	}

	s.logger.Info("channel sender approved", "instance_id", id, "sender_key", senderKey)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleBlockChannelSender blocks a sender.
// POST /api/instances/{id}/channel-access/{senderKey}/block
func (s *Server) handleBlockChannelSender(w http.ResponseWriter, r *http.Request) {
	if !s.hasManager() || s.accessChecker == nil {
		http.Error(w, "no instance manager", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if !s.validateInstance(w, id) {
		return
	}
	senderKey, err := url.PathUnescape(r.PathValue("senderKey"))
	if err != nil {
		http.Error(w, "invalid sender key", http.StatusBadRequest)
		return
	}

	if err := s.accessChecker.UpdateSenderStatus(id, senderKey, config.ChannelAccessBlocked); err != nil {
		s.writeSenderError(w, id, err)
		return
	}

	s.logger.Info("channel sender blocked", "instance_id", id, "sender_key", senderKey)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDismissChannelSender removes a sender entry.
// DELETE /api/instances/{id}/channel-access/{senderKey}
func (s *Server) handleDismissChannelSender(w http.ResponseWriter, r *http.Request) {
	if !s.hasManager() || s.accessChecker == nil {
		http.Error(w, "no instance manager", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if !s.validateInstance(w, id) {
		return
	}
	senderKey, err := url.PathUnescape(r.PathValue("senderKey"))
	if err != nil {
		http.Error(w, "invalid sender key", http.StatusBadRequest)
		return
	}

	if err := s.accessChecker.RemoveSender(id, senderKey); err != nil {
		s.writeSenderError(w, id, err)
		return
	}

	s.logger.Info("channel sender dismissed", "instance_id", id, "sender_key", senderKey)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleGlobalPendingChannelAccess returns the global count and list of pending senders.
// Reads config files directly without per-instance locks — safe because
// SaveInstanceConfig uses atomic rename. Results are best-effort/eventually-consistent.
// GET /api/channel-access/pending
func (s *Server) handleGlobalPendingChannelAccess(w http.ResponseWriter, _ *http.Request) {
	if !s.hasManager() {
		http.Error(w, "no instance manager", http.StatusServiceUnavailable)
		return
	}

	instances := s.manager.ListInstances()
	var items []channelSenderJSON
	for _, inst := range instances {
		instDir := s.manager.InstanceDir(inst.ID)
		cfg, err := config.LoadInstanceConfig(instDir)
		if err != nil || cfg.Channels == nil {
			continue
		}
		for _, sender := range cfg.Channels.SendersByStatus(config.ChannelAccessPending) {
			items = append(items, senderToJSON(sender, inst.ID))
		}
	}
	if items == nil {
		items = []channelSenderJSON{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"count": len(items),
		"items": items,
	})
}

// validateInstance checks that the instance ID exists in the manager.
// Returns false and writes a 404 response if not found.
func (s *Server) validateInstance(w http.ResponseWriter, id string) bool {
	if _, ok := s.manager.GetInstance(id); !ok {
		http.Error(w, "instance not found", http.StatusNotFound)
		return false
	}
	return true
}

// writeSenderError maps common errors to HTTP responses.
func (s *Server) writeSenderError(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, channel.ErrSenderNotFound) {
		http.Error(w, "sender not found", http.StatusNotFound)
		return
	}
	s.logger.Error("failed to update sender", "id", id, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

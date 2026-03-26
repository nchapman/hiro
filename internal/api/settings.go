package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/nchapman/hivebot/internal/agent"
	"github.com/nchapman/hivebot/internal/controlplane"
)

type settingsResponse struct {
	DefaultProvider string `json:"default_provider"`
	DefaultModel    string `json:"default_model"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	if s.cp == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, settingsResponse{
		DefaultProvider: s.cp.DefaultProvider(),
		DefaultModel:    s.cp.DefaultModel(),
	})
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if s.cp == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}

	var req settingsResponse
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	s.cp.SetDefaultProvider(req.DefaultProvider)
	s.cp.SetDefaultModel(req.DefaultModel)
	if err := s.cp.Save(); err != nil {
		s.logger.Warn("failed to save config after settings update", "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleListProviders(w http.ResponseWriter, _ *http.Request) {
	if s.cp == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, s.cp.ListProviders())
}

func (s *Server) handlePutProvider(w http.ResponseWriter, r *http.Request) {
	if s.cp == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	providerType := r.PathValue("type")
	if providerType == "" {
		http.Error(w, "provider type required", http.StatusBadRequest)
		return
	}

	var req struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// If updating and no new key provided, keep the old one.
	if req.APIKey == "" {
		if existing, ok := s.cp.GetProvider(providerType); ok {
			req.APIKey = existing.APIKey
		}
	}

	if req.APIKey == "" {
		http.Error(w, "api_key is required", http.StatusBadRequest)
		return
	}

	s.cp.SetProvider(providerType, controlplane.ProviderConfig{
		APIKey: req.APIKey,
	})

	// If this is the only provider, make it the default.
	providers := s.cp.ListProviders()
	if len(providers) == 1 {
		s.cp.SetDefaultProvider(providerType)
	}
	if err := s.cp.Save(); err != nil {
		s.logger.Warn("failed to save config after provider update", "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	if s.cp == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	providerType := r.PathValue("type")
	if providerType == "" {
		http.Error(w, "provider type required", http.StatusBadRequest)
		return
	}

	// Prevent deleting the only configured provider.
	providers := s.cp.ListProviders()
	if len(providers) <= 1 {
		http.Error(w, "cannot delete the only provider", http.StatusConflict)
		return
	}

	s.cp.DeleteProvider(providerType)
	if err := s.cp.Save(); err != nil {
		s.logger.Warn("failed to save config after provider delete", "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleTestProviderByType(w http.ResponseWriter, r *http.Request) {
	if s.cp == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	providerType := r.PathValue("type")
	provider, ok := s.cp.GetProvider(providerType)
	if !ok {
		http.Error(w, "provider not found", http.StatusNotFound)
		return
	}

	model := testModelForProvider(providerType)
	if model == "" {
		http.Error(w, "unsupported provider type", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if err := agent.TestProviderConnection(ctx, agent.ProviderType(providerType), provider.APIKey, model); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"valid": false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"valid": true})
}

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/nchapman/hiro/internal/controlplane"
	"github.com/nchapman/hiro/internal/models"
	"github.com/nchapman/hiro/internal/provider"
)

type settingsResponse struct {
	DefaultModel string `json:"default_model"` // "provider/model" format
}

func (s *Server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	if s.cp == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, settingsResponse{
		DefaultModel: s.cp.DefaultModelSpec().String(),
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

	spec := models.ParseModelSpec(req.DefaultModel)
	s.cp.SetDefaultModelSpec(spec)
	s.logger.Info("settings updated", "default_model", spec.String())
	if err := s.cp.Save(); err != nil {
		s.logger.Warn("failed to save config after settings update", "error", err)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "warning": "saved in memory but failed to persist to disk"})
		return
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
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// If updating and no new key/URL provided, keep the old values.
	if existing, ok := s.cp.GetProvider(providerType); ok {
		if req.APIKey == "" {
			req.APIKey = existing.APIKey
		}
		if req.BaseURL == "" {
			req.BaseURL = existing.BaseURL
		}
	}

	if req.APIKey == "" {
		http.Error(w, "api_key is required", http.StatusBadRequest)
		return
	}

	if err := s.cp.SetProvider(providerType, controlplane.ProviderConfig{
		APIKey:  req.APIKey,
		BaseURL: req.BaseURL,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.logger.Info("provider configured", "provider", providerType)

	// When this is the only provider and no default model is set,
	// ProviderInfo() will auto-resolve to this provider.
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
	if len(s.cp.ConfiguredProviderTypes()) <= 1 {
		http.Error(w, "cannot delete the only provider", http.StatusConflict)
		return
	}

	s.cp.DeleteProvider(providerType)
	s.logger.Info("provider deleted", "provider", providerType)
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
	prov, ok := s.cp.GetProvider(providerType)
	if !ok {
		http.Error(w, "provider not found", http.StatusNotFound)
		return
	}

	model := provider.TestModelForProvider(providerType)
	if model == "" {
		http.Error(w, "unsupported provider type", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := provider.TestConnection(ctx, provider.Type(providerType), prov.APIKey, prov.BaseURL, model); err != nil {
		msg := err.Error()
		if ctx.Err() != nil {
			msg = "provider did not respond within 30s"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"valid": false,
			"error": msg,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"valid": true})
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	providerFilter := r.URL.Query().Get("provider")
	if providerFilter != "" {
		writeJSON(w, http.StatusOK, models.ModelsForProvider(providerFilter))
		return
	}
	// No provider specified: return models from all configured providers.
	if s.cp != nil {
		configured := s.cp.ConfiguredProviderTypes()
		if len(configured) > 0 {
			writeJSON(w, http.StatusOK, models.ModelsForProviders(configured))
			return
		}
	}
	writeJSON(w, http.StatusOK, []models.ModelInfo{})
}

func (s *Server) handleListProviderTypes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, provider.AvailableProviders())
}

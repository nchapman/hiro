package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/nchapman/hivebot/internal/agent"
	"github.com/nchapman/hivebot/internal/controlplane"
)

type setupRequest struct {
	Password        string `json:"password"`
	ProviderType    string `json:"provider_type"`
	APIKey          string `json:"api_key"`
	DefaultModel    string `json:"default_model"`
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.cp == nil {
		http.Error(w, "control plane not configured", http.StatusServiceUnavailable)
		return
	}
	if !s.cp.NeedsSetup() {
		http.Error(w, "setup already complete", http.StatusConflict)
		return
	}
	if !isSameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var req setupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.Password) < 8 {
		http.Error(w, "password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	if req.ProviderType == "" || req.APIKey == "" {
		http.Error(w, "provider_type and api_key are required", http.StatusBadRequest)
		return
	}

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		s.logger.Error("failed to hash password", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Apply config
	s.cp.SetPasswordHash(string(hash))
	s.cp.SetProvider(req.ProviderType, controlplane.ProviderConfig{
		APIKey: req.APIKey,
	})
	s.cp.SetDefaultProvider(req.ProviderType)
	if req.DefaultModel != "" {
		s.cp.SetDefaultModel(req.DefaultModel)
	}

	// Persist immediately
	if err := s.cp.Save(); err != nil {
		s.logger.Error("failed to save config after setup", "error", err)
		http.Error(w, "failed to save configuration", http.StatusInternalServerError)
		return
	}

	// Start the agent manager
	if s.startManager != nil {
		if err := s.startManager(); err != nil {
			s.logger.Error("failed to start manager after setup", "error", err)
		}
	}

	// Create a signed session token so the user is logged in
	signer := s.tokenSigner()
	if signer == nil {
		s.logger.Error("failed to get token signer after setup")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	token := signer.Create()

	http.SetCookie(w, &http.Cookie{
		Name:     "hive_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400, // 24 hours
	})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	// Only available during initial setup; after setup, use the authenticated
	// /api/settings/providers/{type}/test endpoint instead.
	if s.cp != nil && !s.cp.NeedsSetup() {
		http.Error(w, "setup already complete; use /api/settings/providers/{type}/test", http.StatusConflict)
		return
	}
	if !isSameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var req struct {
		Type   string `json:"type"`
		APIKey string `json:"api_key"`
		Model  string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Type == "" || req.APIKey == "" {
		http.Error(w, "type and api_key are required", http.StatusBadRequest)
		return
	}

	model := req.Model
	if model == "" {
		model = agent.TestModelForProvider(req.Type)
		if model == "" {
			http.Error(w, "unsupported provider type", http.StatusBadRequest)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if err := agent.TestProviderConnection(ctx, agent.ProviderType(req.Type), req.APIKey, "", model); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"valid": false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"valid": true})
}


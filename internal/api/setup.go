package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/nchapman/hiro/internal/cluster"
	"github.com/nchapman/hiro/internal/controlplane"
	"github.com/nchapman/hiro/internal/models"
	"github.com/nchapman/hiro/internal/provider"
)

const defaultTrackerURL = "https://discover.hellohiro.ai"

type setupRequest struct {
	Password string `json:"password"`
	Mode     string `json:"mode"`                   // "standalone", "leader", or "worker"
	NodeName string `json:"node_name,omitempty"`     // human-friendly machine name (leader + worker)

	// Provider (standalone + leader only; workers get this from the leader)
	ProviderType string `json:"provider_type,omitempty"`
	APIKey       string `json:"api_key,omitempty"`
	DefaultModel string `json:"default_model,omitempty"`

	// Worker-specific (one of swarm code or direct connection)
	WorkerSwarmCode  string `json:"worker_swarm_code,omitempty"`
	WorkerLeaderAddr string `json:"worker_leader_addr,omitempty"`
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
	if !isLoopbackOrigin(r) {
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

	// Validate mode
	switch req.Mode {
	case "standalone", "leader", "worker":
		// ok
	case "":
		http.Error(w, "mode is required", http.StatusBadRequest)
		return
	default:
		http.Error(w, "mode must be standalone, leader, or worker", http.StatusBadRequest)
		return
	}

	// Standalone and leader require a provider; worker does not.
	if req.Mode != "worker" {
		if req.ProviderType == "" || req.APIKey == "" {
			http.Error(w, "provider_type and api_key are required", http.StatusBadRequest)
			return
		}
	}

	// Worker requires either a swarm code or a direct leader address.
	if req.Mode == "worker" {
		if req.WorkerSwarmCode == "" && req.WorkerLeaderAddr == "" {
			http.Error(w, "worker mode requires worker_swarm_code or worker_leader_addr", http.StatusBadRequest)
			return
		}
	}

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		s.logger.Error("failed to hash password", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Validate provider config before mutating any state, so a bad provider
	// type doesn't leave the control plane in a half-configured state.
	if req.Mode != "worker" {
		if err := s.cp.SetProvider(req.ProviderType, controlplane.ProviderConfig{
			APIKey: req.APIKey,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.DefaultModel != "" {
			spec := models.ParseModelSpec(req.DefaultModel)
			if spec.Provider == "" {
				spec.Provider = req.ProviderType
			}
			s.cp.SetDefaultModelSpec(spec)
		} else {
			s.cp.SetDefaultModelSpec(models.ModelSpec{Provider: req.ProviderType})
		}
	}

	// Apply common config — all validation has passed at this point.
	s.cp.SetPasswordHash(string(hash))
	s.cp.SetClusterMode(req.Mode)

	nodeName := req.NodeName
	if nodeName == "" {
		if h, err := os.Hostname(); err == nil {
			nodeName = h
		}
	}
	if nodeName != "" {
		s.cp.SetClusterNodeName(nodeName)
	}

	// Apply mode-specific cluster config
	resp := map[string]any{"ok": true}

	switch req.Mode {
	case "leader":
		swarmCode, err := cluster.GenerateSwarmCode()
		if err != nil {
			s.logger.Error("failed to generate swarm code", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		s.cp.SetClusterTrackerURL(defaultTrackerURL)
		s.cp.SetClusterSwarmCode(swarmCode)
		resp["swarm_code"] = swarmCode

	case "worker":
		if req.WorkerSwarmCode != "" {
			s.cp.SetClusterTrackerURL(defaultTrackerURL)
			s.cp.SetClusterSwarmCode(req.WorkerSwarmCode)
		}
		if req.WorkerLeaderAddr != "" {
			s.cp.SetClusterLeaderAddr(req.WorkerLeaderAddr)
		}
		resp["restart_required"] = true
	}

	// Persist immediately
	if err := s.cp.Save(); err != nil {
		s.logger.Error("failed to save config after setup", "error", err)
		http.Error(w, "failed to save configuration", http.StatusInternalServerError)
		return
	}

	// Start services based on mode (worker restart is deferred until after response).
	needsRestart := false
	switch req.Mode {
	case "standalone":
		if s.startManager != nil {
			if err := s.startManager(); err != nil {
				s.logger.Error("failed to start manager after setup", "error", err)
			}
		}
	case "leader":
		// Cluster must start before manager so the manager gets the cluster service.
		if s.startCluster != nil {
			if err := s.startCluster(); err != nil {
				s.logger.Error("failed to start cluster after setup", "error", err)
			}
		}
		if s.startManager != nil {
			if err := s.startManager(); err != nil {
				s.logger.Error("failed to start manager after setup", "error", err)
			}
		}
	case "worker":
		needsRestart = true
	}

	// Create a signed session token so the user is logged in
	signer := s.tokenSigner()
	if signer == nil {
		s.logger.Error("failed to get token signer after setup")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	token := signer.Create()
	setSessionCookie(w, r, token)

	writeJSON(w, http.StatusOK, resp)

	// Request restart AFTER the response is written to avoid a race where
	// httpServer.Shutdown() interrupts the in-flight response.
	if needsRestart && s.requestRestart != nil {
		go func() {
			time.Sleep(100 * time.Millisecond)
			s.requestRestart()
		}()
	}
}

func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	// Only available during initial setup; after setup, use the authenticated
	// /api/settings/providers/{type}/test endpoint instead.
	if s.cp != nil && !s.cp.NeedsSetup() {
		http.Error(w, "setup already complete; use /api/settings/providers/{type}/test", http.StatusConflict)
		return
	}
	if !isLoopbackOrigin(r) {
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
		model = provider.TestModelForProvider(req.Type)
		if model == "" {
			http.Error(w, "unsupported provider type", http.StatusBadRequest)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if err := provider.TestConnection(ctx, provider.Type(req.Type), req.APIKey, "", model); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"valid": false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"valid": true})
}

func (s *Server) handleValidateSwarm(w http.ResponseWriter, r *http.Request) {
	if s.cp != nil && !s.cp.NeedsSetup() {
		http.Error(w, "setup already complete", http.StatusConflict)
		return
	}
	if !isLoopbackOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var req struct {
		SwarmCode string `json:"swarm_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.SwarmCode == "" {
		http.Error(w, "swarm_code is required", http.StatusBadRequest)
		return
	}

	// Create/load the node's real identity for the tracker probe.
	identity, err := cluster.LoadOrCreateIdentity(s.rootDir)
	if err != nil {
		s.logger.Error("failed to load node identity for swarm check", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	result, err := cluster.CheckSwarm(ctx, defaultTrackerURL, req.SwarmCode, identity, s.logger)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"leader_found": false,
			"error":        err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, result)
}


package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nchapman/hiro/internal/agent"
)

// instanceConfigResponse is the JSON shape for GET /api/instances/{id}/config.
type instanceConfigResponse struct {
	Model           string   `json:"model"`
	ReasoningEffort string   `json:"reasoning_effort"`
	AllowedTools    []string `json:"allowed_tools"`
	DisallowedTools []string `json:"disallowed_tools"`
}

// instanceConfigRequest is the JSON shape for PUT /api/instances/{id}/config.
// Pointer-to-slice fields distinguish "not sent" (nil) from "explicitly empty" ([]).
type instanceConfigRequest struct {
	Model           string    `json:"model,omitempty"`
	ReasoningEffort *string   `json:"reasoning_effort,omitempty"`
	AllowedTools    *[]string `json:"allowed_tools,omitempty"`
	DisallowedTools *[]string `json:"disallowed_tools,omitempty"`
}

func (s *Server) handleGetInstanceConfig(w http.ResponseWriter, r *http.Request) {
	if !s.hasManager() {
		http.Error(w, "no instance manager", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	cfg, err := s.manager.GetInstanceConfig(id)
	if err != nil {
		if errors.Is(err, agent.ErrInstanceNotFound) {
			http.Error(w, "instance not found", http.StatusNotFound)
		} else {
			s.logger.Error("failed to read instance config", "id", id, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	// Use the resolved model from InstanceInfo when the config has no
	// explicit override — empty model in config means "use the default",
	// and we want to show what's actually in effect.
	model := cfg.Model
	if model == "" {
		if info, ok := s.manager.GetInstance(id); ok && info.Model != "" {
			model = info.Model
		}
	}

	resp := instanceConfigResponse{
		Model:           model,
		ReasoningEffort: cfg.ReasoningEffort,
		AllowedTools:    cfg.AllowedTools,
		DisallowedTools: cfg.DisallowedTools,
	}
	// Return empty arrays instead of null for cleaner JSON.
	if resp.AllowedTools == nil {
		resp.AllowedTools = []string{}
	}
	if resp.DisallowedTools == nil {
		resp.DisallowedTools = []string{}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePutInstanceConfig(w http.ResponseWriter, r *http.Request) {
	if !s.hasManager() {
		http.Error(w, "no instance manager", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")

	var req instanceConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Dereference pointer-to-slice fields: nil means "not sent" (pass nil),
	// non-nil means "explicitly set" (pass the slice, even if empty).
	var allowedTools, disallowedTools []string
	if req.AllowedTools != nil {
		allowedTools = *req.AllowedTools
	}
	if req.DisallowedTools != nil {
		disallowedTools = *req.DisallowedTools
	}

	info, ok := s.manager.GetInstance(id)
	if !ok {
		http.Error(w, "instance not found", http.StatusNotFound)
		return
	}

	var err error
	if info.Status == agent.InstanceStatusRunning {
		err = s.manager.UpdateInstanceConfig(r.Context(), id, req.Model, req.ReasoningEffort, allowedTools, disallowedTools)
	} else {
		err = s.manager.UpdateStoppedInstanceConfig(id, req.Model, req.ReasoningEffort, allowedTools, disallowedTools)
	}

	if err != nil {
		switch {
		case errors.Is(err, agent.ErrInstanceNotFound):
			http.Error(w, "instance not found", http.StatusNotFound)
		case errors.Is(err, agent.ErrInstanceStopped), errors.Is(err, agent.ErrInstanceNotStopped):
			http.Error(w, "instance status changed; please try again", http.StatusConflict)
		default:
			s.logger.Error("failed to update instance config", "id", id, "error", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

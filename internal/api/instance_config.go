package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/nchapman/hiro/internal/agent"
	"github.com/nchapman/hiro/internal/config"
)

// instanceConfigResponse is the JSON shape for GET /api/instances/{id}/config.
type instanceConfigResponse struct {
	Model           string   `json:"model"`
	ReasoningEffort string   `json:"reasoning_effort"`
	AllowedTools    []string `json:"allowed_tools"`
	DisallowedTools []string `json:"disallowed_tools"`
	PersonaName     string   `json:"persona_name"`
	PersonaDesc     string   `json:"persona_description"`
	PersonaBody     string   `json:"persona_body"`
	Memory          string   `json:"memory"`
}

// instanceConfigRequest is the JSON shape for PUT /api/instances/{id}/config.
// Pointer-to-slice fields distinguish "not sent" (nil) from "explicitly empty" ([]).
type instanceConfigRequest struct {
	Model           string    `json:"model,omitempty"`
	ReasoningEffort *string   `json:"reasoning_effort,omitempty"`
	AllowedTools    *[]string `json:"allowed_tools,omitempty"`
	DisallowedTools *[]string `json:"disallowed_tools,omitempty"`
	PersonaName     *string   `json:"persona_name,omitempty"`
	PersonaDesc     *string   `json:"persona_description,omitempty"`
	PersonaBody     *string   `json:"persona_body,omitempty"`
	Memory          *string   `json:"memory,omitempty"`
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

	// Read persona.md and memory.md from the instance directory.
	instDir := s.manager.InstanceDir(id)
	persona, _ := config.ReadPersonaFile(instDir)
	memory, _ := config.ReadMemoryFile(instDir)

	resp := instanceConfigResponse{
		Model:           model,
		ReasoningEffort: cfg.ReasoningEffort,
		AllowedTools:    cfg.AllowedTools,
		DisallowedTools: cfg.DisallowedTools,
		PersonaName:     persona.Name,
		PersonaDesc:     persona.Description,
		PersonaBody:     persona.Body,
		Memory:          memory,
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

	if err := s.applyInstanceFileUpdates(id, req); err != nil {
		s.logger.Error("failed to write instance files", "id", id, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// applyInstanceFileUpdates writes persona.md and memory.md changes from a config request.
func (s *Server) applyInstanceFileUpdates(id string, req instanceConfigRequest) error {
	instDir := s.manager.InstanceDir(id)

	if req.PersonaName != nil || req.PersonaDesc != nil || req.PersonaBody != nil {
		existing, _ := config.ReadPersonaFile(instDir)
		name, desc, body := existing.Name, existing.Description, existing.Body
		if req.PersonaName != nil {
			name = *req.PersonaName
		}
		if req.PersonaDesc != nil {
			desc = *req.PersonaDesc
		}
		if req.PersonaBody != nil {
			body = *req.PersonaBody
		}
		if err := config.WritePersonaFile(instDir, name, desc, body); err != nil {
			return fmt.Errorf("writing persona.md: %w", err)
		}
	}

	if req.Memory != nil {
		if err := config.WriteMemoryFile(instDir, *req.Memory); err != nil {
			return fmt.Errorf("writing memory.md: %w", err)
		}
	}

	return nil
}

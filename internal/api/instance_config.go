package api

import (
	"context"
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

	// Channel config — uses secret references (e.g. ${SLACK_BOT}), not resolved values.
	Telegram *telegramConfigJSON `json:"telegram,omitempty"`
	Slack    *slackConfigJSON    `json:"slack,omitempty"`
}

type telegramConfigJSON struct {
	BotToken     string  `json:"bot_token"`
	AllowedChats []int64 `json:"allowed_chats,omitempty"`
}

type slackConfigJSON struct {
	BotToken        string   `json:"bot_token"`
	SigningSecret   string   `json:"signing_secret"`
	AllowedChannels []string `json:"allowed_channels,omitempty"`
}

// maskToken returns "••••last4" for display, or empty if the value is empty.
func maskToken(s string) string {
	const maskSuffix = 4
	if s == "" {
		return ""
	}
	if len(s) <= maskSuffix {
		return "••••"
	}
	return "••••" + s[len(s)-maskSuffix:]
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

	// Channel config — pointer so nil = "not sent", non-nil = "set/update".
	// Send with empty bot_token to remove a channel.
	Telegram *telegramConfigJSON `json:"telegram,omitempty"`
	Slack    *slackConfigJSON    `json:"slack,omitempty"`
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

	writeJSON(w, http.StatusOK, s.buildConfigResponse(id, cfg))
}

// buildConfigResponse assembles the GET response from config.yaml, persona.md,
// memory.md, and InstanceInfo.
func (s *Server) buildConfigResponse(id string, cfg config.InstanceConfig) instanceConfigResponse {
	model := cfg.Model
	if model == "" {
		if info, ok := s.manager.GetInstance(id); ok && info.Model != "" {
			model = info.Model
		}
	}

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
	if resp.AllowedTools == nil {
		resp.AllowedTools = []string{}
	}
	if resp.DisallowedTools == nil {
		resp.DisallowedTools = []string{}
	}

	if ch := cfg.Channels; ch != nil {
		if tg := ch.Telegram; tg != nil {
			resp.Telegram = &telegramConfigJSON{BotToken: maskToken(tg.BotToken), AllowedChats: tg.AllowedChats}
		}
		if sl := ch.Slack; sl != nil {
			resp.Slack = &slackConfigJSON{BotToken: maskToken(sl.BotToken), SigningSecret: maskToken(sl.SigningSecret), AllowedChannels: sl.AllowedChannels}
		}
	}
	return resp
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

	if err := s.applyInstanceFileUpdates(r.Context(), id, info, req); err != nil {
		s.logger.Error("failed to write instance files", "id", id, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// applyInstanceFileUpdates writes persona.md, memory.md, and channel config changes.
func (s *Server) applyInstanceFileUpdates(ctx context.Context, id string, info agent.InstanceInfo, req instanceConfigRequest) error {
	instDir := s.manager.InstanceDir(id)

	if err := applyPersonaUpdate(instDir, req); err != nil {
		return err
	}

	if req.Memory != nil {
		if err := config.WriteMemoryFile(instDir, *req.Memory); err != nil {
			return fmt.Errorf("writing memory.md: %w", err)
		}
	}

	if req.Telegram != nil || req.Slack != nil {
		if err := s.applyChannelUpdate(ctx, id, instDir, info, req); err != nil {
			return err
		}
	}

	return nil
}

// applyPersonaUpdate writes persona.md if any persona field was sent.
func applyPersonaUpdate(instDir string, req instanceConfigRequest) error {
	if req.PersonaName == nil && req.PersonaDesc == nil && req.PersonaBody == nil {
		return nil
	}
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
	return nil
}

// applyChannelUpdate writes channel config to config.yaml and restarts channel
// bindings for running instances.
func (s *Server) applyChannelUpdate(ctx context.Context, id, instDir string, info agent.InstanceInfo, req instanceConfigRequest) error {
	existing, _ := config.LoadInstanceConfig(instDir)
	if existing.Channels == nil {
		existing.Channels = &config.InstanceChannelsConfig{}
	}

	if req.Telegram != nil {
		existing.Channels.Telegram = mapTelegramConfig(req.Telegram)
	}
	if req.Slack != nil {
		existing.Channels.Slack = mapSlackConfig(req.Slack)
	}
	if existing.Channels.Telegram == nil && existing.Channels.Slack == nil {
		existing.Channels = nil
	}

	if err := config.SaveInstanceConfig(instDir, existing); err != nil {
		return fmt.Errorf("writing channel config: %w", err)
	}

	if info.Status == agent.InstanceStatusRunning {
		s.manager.RestartChannels(ctx, id)
	}
	return nil
}

// mapTelegramConfig converts JSON request to config struct. Empty bot_token removes the channel.
func mapTelegramConfig(j *telegramConfigJSON) *config.InstanceTelegramConfig {
	if j.BotToken == "" {
		return nil
	}
	return &config.InstanceTelegramConfig{BotToken: j.BotToken, AllowedChats: j.AllowedChats}
}

// mapSlackConfig converts JSON request to config struct. Empty bot_token removes the channel.
func mapSlackConfig(j *slackConfigJSON) *config.InstanceSlackConfig {
	if j.BotToken == "" {
		return nil
	}
	return &config.InstanceSlackConfig{BotToken: j.BotToken, SigningSecret: j.SigningSecret, AllowedChannels: j.AllowedChannels}
}

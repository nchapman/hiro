package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/nchapman/hiro/internal/models"
)

// UsageInfo is the JSON shape for session usage data.
// It includes per-turn totals, last-step context data, and cumulative session totals.
type UsageInfo struct {
	// Per-turn totals (summed across all steps in the most recent turn).
	TurnInputTokens  int64   `json:"turn_input_tokens"`
	TurnOutputTokens int64   `json:"turn_output_tokens"`
	TurnCost         float64 `json:"turn_cost"`

	// Last step context (the final LLM call — reflects actual context window usage).
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`

	// Cumulative session totals.
	SessionInputTokens  int64   `json:"session_input_tokens"`
	SessionOutputTokens int64   `json:"session_output_tokens"`
	SessionTotalTokens  int64   `json:"session_total_tokens"`
	SessionCost         float64 `json:"session_cost"`
	EventCount          int64   `json:"event_count"`

	// Model info.
	ContextWindow int    `json:"context_window"`
	Model         string `json:"model,omitempty"`
}

func (s *Server) handleInstanceUsage(w http.ResponseWriter, r *http.Request) {
	if s.pdb == nil {
		http.Error(w, "usage tracking unavailable", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")

	// Look up instance to get the model and verify it exists.
	var model string
	if s.manager == nil {
		http.Error(w, "manager not available", http.StatusServiceUnavailable)
		return
	}
	info, ok := s.manager.GetInstance(id)
	if !ok {
		http.Error(w, "instance not found", http.StatusNotFound)
		return
	}
	model = info.Model

	// Try query param, then fall back to web session for usage tracking.
	sessionID := r.URL.Query().Get("session_id")
	if sessionID != "" {
		// Validate the supplied session belongs to this instance.
		sess, err := s.pdb.GetSession(r.Context(), sessionID)
		if err != nil || sess.InstanceID != id {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
	} else {
		sessionID = s.manager.SessionIDForChannel(id, "web")
	}
	if sessionID == "" {
		sessionID = id // fallback
	}
	writeJSON(w, http.StatusOK, s.buildUsageInfoForSession(r.Context(), sessionID, model))
}

// buildUsageInfoForSession constructs a UsageInfo from the DB for a given session.
func (s *Server) buildUsageInfoForSession(ctx context.Context, sessionID, model string) UsageInfo {
	info := UsageInfo{
		ContextWindow: models.ContextWindow(model),
		Model:         model,
	}

	if s.pdb == nil {
		return info
	}

	// Cumulative session totals.
	if usage, err := s.pdb.GetSessionUsage(ctx, sessionID); err == nil {
		info.SessionInputTokens = usage.TotalInputTokens
		info.SessionOutputTokens = usage.TotalOutputTokens
		info.SessionTotalTokens = usage.TotalInputTokens + usage.TotalOutputTokens
		info.SessionCost = usage.TotalCost
		info.EventCount = usage.EventCount
	}

	// Per-turn totals (all steps in the most recent turn).
	if turn, ok, err := s.pdb.GetLastTurnUsage(ctx, sessionID); err == nil && ok {
		info.TurnInputTokens = turn.TotalInputTokens
		info.TurnOutputTokens = turn.TotalOutputTokens
		info.TurnCost = turn.TotalCost
	}

	// Last step (actual context window usage from the final LLM call).
	if last, ok, err := s.pdb.GetLastUsageEvent(ctx, sessionID); err == nil && ok {
		info.PromptTokens = last.InputTokens
		info.CompletionTokens = last.OutputTokens
	}

	return info
}

func (s *Server) handleTotalUsage(w http.ResponseWriter, r *http.Request) {
	if s.pdb == nil {
		http.Error(w, "usage tracking unavailable", http.StatusServiceUnavailable)
		return
	}
	usage, err := s.pdb.GetTotalUsage(r.Context())
	if err != nil {
		s.logger.Error("failed to get total usage", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func (s *Server) handleUsageByModel(w http.ResponseWriter, r *http.Request) {
	if s.pdb == nil {
		http.Error(w, "usage tracking unavailable", http.StatusServiceUnavailable)
		return
	}
	usage, err := s.pdb.GetUsageByModel(r.Context())
	if err != nil {
		s.logger.Error("failed to get usage by model", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func (s *Server) handleUsageByDay(w http.ResponseWriter, r *http.Request) {
	if s.pdb == nil {
		http.Error(w, "usage tracking unavailable", http.StatusServiceUnavailable)
		return
	}
	limit := 30
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	usage, err := s.pdb.GetUsageByDay(r.Context(), limit)
	if err != nil {
		s.logger.Error("failed to get usage by day", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

package api

import (
	"net/http"
	"strconv"

	"github.com/nchapman/hivebot/internal/models"
)

// UsageInfo is the JSON shape for session usage data.
// It includes both per-turn data (from the most recent LLM call) and
// cumulative session totals.
type UsageInfo struct {
	// Per-turn data (most recent LLM call).
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TurnTotal        int64   `json:"turn_total"`
	TurnCost         float64 `json:"turn_cost"`

	// Cumulative session totals.
	SessionInputTokens  int64   `json:"session_input_tokens"`
	SessionOutputTokens int64   `json:"session_output_tokens"`
	SessionTotalTokens  int64   `json:"session_total_tokens"`
	SessionCost         float64 `json:"session_cost"`
	EventCount          int64   `json:"event_count"`

	// Model info.
	ContextWindow int `json:"context_window"`
	Model         string `json:"model,omitempty"`
}

func (s *Server) handleSessionUsage(w http.ResponseWriter, r *http.Request) {
	if s.pdb == nil {
		http.Error(w, "usage tracking unavailable", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")

	// Look up session to get the model and verify it exists.
	var model string
	if s.manager != nil {
		info, ok := s.manager.GetSession(id)
		if !ok {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		model = info.Model
	}

	writeJSON(w, http.StatusOK, s.buildUsageInfoForSession(id, model))
}

// buildUsageInfoForSession constructs a UsageInfo from the DB for a given session.
func (s *Server) buildUsageInfoForSession(sessionID, model string) UsageInfo {
	info := UsageInfo{
		ContextWindow: models.ContextWindow(model),
		Model:         model,
	}

	if s.pdb == nil {
		return info
	}

	// Cumulative totals.
	if usage, err := s.pdb.GetSessionUsage(sessionID); err == nil {
		info.SessionInputTokens = usage.TotalInputTokens
		info.SessionOutputTokens = usage.TotalOutputTokens
		info.SessionTotalTokens = usage.TotalInputTokens + usage.TotalOutputTokens
		info.SessionCost = usage.TotalCost
		info.EventCount = usage.EventCount
	}

	// Per-turn data from the most recent event.
	if last, ok, err := s.pdb.GetLastUsageEvent(sessionID); err == nil && ok {
		info.PromptTokens = last.InputTokens
		info.CompletionTokens = last.OutputTokens
		info.TurnTotal = last.InputTokens + last.OutputTokens
		info.TurnCost = last.Cost
	}

	return info
}

func (s *Server) handleTotalUsage(w http.ResponseWriter, _ *http.Request) {
	if s.pdb == nil {
		http.Error(w, "usage tracking unavailable", http.StatusServiceUnavailable)
		return
	}
	usage, err := s.pdb.GetTotalUsage()
	if err != nil {
		s.logger.Error("failed to get total usage", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func (s *Server) handleUsageByModel(w http.ResponseWriter, _ *http.Request) {
	if s.pdb == nil {
		http.Error(w, "usage tracking unavailable", http.StatusServiceUnavailable)
		return
	}
	usage, err := s.pdb.GetUsageByModel()
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
	usage, err := s.pdb.GetUsageByDay(limit)
	if err != nil {
		s.logger.Error("failed to get usage by day", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

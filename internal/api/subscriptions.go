package api

import (
	"errors"
	"net/http"
	"time"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

func (s *Server) subscriptionRoutes() {
	s.mux.HandleFunc("GET /api/subscriptions", s.requireAuth(s.handleListSubscriptions))
	s.mux.HandleFunc("DELETE /api/subscriptions/{id}", s.requireAuth(s.handleDeleteSubscription))
	s.mux.HandleFunc("POST /api/subscriptions/{id}/pause", s.requireAuth(s.handlePauseSubscription))
	s.mux.HandleFunc("POST /api/subscriptions/{id}/resume", s.requireAuth(s.handleResumeSubscription))
}

// subscriptionResponse is the JSON shape returned to the frontend.
type subscriptionResponse struct {
	ID           string  `json:"id"`
	InstanceID   string  `json:"instance_id"`
	InstanceName string  `json:"instance_name,omitempty"`
	Name         string  `json:"name"`
	TriggerType  string  `json:"trigger_type"`
	Schedule     string  `json:"schedule"`
	Message      string  `json:"message"`
	Status       string  `json:"status"`
	NextFire     *string `json:"next_fire"`
	LastFired    *string `json:"last_fired"`
	FireCount    int     `json:"fire_count"`
	ErrorCount   int     `json:"error_count"`
	LastError    string  `json:"last_error,omitempty"`
	CreatedAt    string  `json:"created_at"`
}

func toSubscriptionResponse(sub platformdb.Subscription, instanceName string) subscriptionResponse {
	r := subscriptionResponse{
		ID:           sub.ID,
		InstanceID:   sub.InstanceID,
		InstanceName: instanceName,
		Name:         sub.Name,
		TriggerType:  sub.Trigger.Type,
		Message:      sub.Message,
		Status:       sub.Status,
		FireCount:    sub.FireCount,
		ErrorCount:   sub.ErrorCount,
		LastError:    sub.LastError,
		CreatedAt:    sub.CreatedAt.UTC().Format(time.RFC3339),
	}
	if sub.Trigger.Type == "cron" {
		r.Schedule = sub.Trigger.Expr
	} else {
		r.Schedule = sub.Trigger.At
	}
	if sub.NextFire != nil {
		s := sub.NextFire.UTC().Format(time.RFC3339)
		r.NextFire = &s
	}
	if sub.LastFired != nil {
		s := sub.LastFired.UTC().Format(time.RFC3339)
		r.LastFired = &s
	}
	return r
}

// handleListSubscriptions returns all subscriptions, optionally filtered by instance_id.
// GET /api/subscriptions?instance_id=xxx
func (s *Server) handleListSubscriptions(w http.ResponseWriter, r *http.Request) {
	if s.pdb == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	instanceID := r.URL.Query().Get("instance_id")

	var subs []platformdb.Subscription
	var err error
	if instanceID != "" {
		subs, err = s.pdb.ListSubscriptionsByInstance(r.Context(), instanceID)
	} else {
		subs, err = s.pdb.ListAllSubscriptions(r.Context())
	}
	if err != nil {
		s.logger.Error("failed to list subscriptions", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Build instance ID → name map for display.
	nameMap := make(map[string]string)
	if s.hasManager() {
		for _, inst := range s.manager.ListInstances() {
			nameMap[inst.ID] = inst.Name
		}
	}

	result := make([]subscriptionResponse, 0, len(subs))
	for _, sub := range subs {
		result = append(result, toSubscriptionResponse(sub, nameMap[sub.InstanceID]))
	}
	writeJSON(w, http.StatusOK, result)
}

// handleDeleteSubscription removes a subscription and notifies the scheduler.
// DELETE /api/subscriptions/{id}
func (s *Server) handleDeleteSubscription(w http.ResponseWriter, r *http.Request) {
	if s.pdb == nil {
		http.Error(w, "no database", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")

	if err := s.pdb.DeleteSubscription(r.Context(), id); err != nil {
		if errors.Is(err, platformdb.ErrNotFound) {
			http.Error(w, "subscription not found", http.StatusNotFound)
			return
		}
		s.logger.Error("failed to delete subscription", "id", id, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Notify the scheduler to remove from the heap.
	if s.hasManager() {
		if sched := s.manager.GetScheduler(); sched != nil {
			sched.Remove(id)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// handlePauseSubscription pauses a subscription.
// POST /api/subscriptions/{id}/pause
func (s *Server) handlePauseSubscription(w http.ResponseWriter, r *http.Request) {
	if s.pdb == nil {
		http.Error(w, "no database", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")

	// Clear next_fire when pausing.
	zero := time.Time{}
	if err := s.pdb.UpdateSubscriptionStatus(r.Context(), id, "paused", &zero); err != nil {
		if errors.Is(err, platformdb.ErrNotFound) {
			http.Error(w, "subscription not found", http.StatusNotFound)
			return
		}
		s.logger.Error("failed to pause subscription", "id", id, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if s.hasManager() {
		if sched := s.manager.GetScheduler(); sched != nil {
			sched.Remove(id)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleResumeSubscription resumes a paused subscription.
// POST /api/subscriptions/{id}/resume
func (s *Server) handleResumeSubscription(w http.ResponseWriter, r *http.Request) {
	if s.pdb == nil {
		http.Error(w, "no database", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")

	// Re-read to get the trigger def for re-adding to the scheduler.
	sub, err := s.pdb.GetSubscription(r.Context(), id)
	if err != nil {
		if errors.Is(err, platformdb.ErrNotFound) {
			http.Error(w, "subscription not found", http.StatusNotFound)
			return
		}
		s.logger.Error("failed to get subscription", "id", id, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := s.pdb.UpdateSubscriptionStatus(r.Context(), id, "active", nil); err != nil {
		s.logger.Error("failed to resume subscription", "id", id, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	sub.Status = "active"
	if s.hasManager() {
		if sched := s.manager.GetScheduler(); sched != nil {
			sched.Add(sub)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// Package api implements the HTTP server that serves the web UI,
// the WebSocket endpoint for worker connections, and the REST API
// for the dashboard.
package api

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/nchapman/hivebot/internal/hub"
)

// Server is the HTTP server for the Hive leader.
type Server struct {
	swarm  *hub.Swarm
	webFS  fs.FS // embedded web UI files (nil = no UI serving)
	mux    *http.ServeMux
	logger *slog.Logger
}

// NewServer creates a new API server. If webFS is non-nil, the web UI
// will be served for any request that doesn't match an API route.
func NewServer(swarm *hub.Swarm, logger *slog.Logger, webFS fs.FS) *Server {
	s := &Server{
		swarm:  swarm,
		webFS:  webFS,
		mux:    http.NewServeMux(),
		logger: logger,
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	// API routes
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/swarm", s.handleSwarmStatus)
	s.mux.HandleFunc("GET /api/workers", s.handleListWorkers)
	s.mux.HandleFunc("GET /api/tasks", s.handleListTasks)

	// TODO: WebSocket endpoint for workers
	// TODO: WebSocket endpoint for web UI chat

	// Catch-all: serve web UI or 404
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Unmatched /api/ paths get a proper 404
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}

		// No web UI embedded — nothing to serve
		if s.webFS == nil {
			http.NotFound(w, r)
			return
		}

		// Try to serve the file directly
		fileServer := http.FileServerFS(s.webFS)
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		if _, err := fs.Stat(s.webFS, path[1:]); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback: serve index.html for unmatched routes
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleSwarmStatus(w http.ResponseWriter, r *http.Request) {
	workers := s.swarm.Workers()
	activeTasks := s.swarm.ActiveTasks()

	writeJSON(w, http.StatusOK, map[string]any{
		"swarm_code":   s.swarm.Code(),
		"worker_count": len(workers),
		"active_tasks": len(activeTasks),
	})
}

func (s *Server) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	workers := s.swarm.Workers()
	type workerInfo struct {
		ID          string   `json:"id"`
		AgentName   string   `json:"agent_name"`
		Description string   `json:"description"`
		Skills      []string `json:"skills"`
		ConnectedAt string   `json:"connected_at"`
	}

	result := make([]workerInfo, 0, len(workers))
	for _, wk := range workers {
		result = append(result, workerInfo{
			ID:          wk.ID,
			AgentName:   wk.AgentName,
			Description: wk.Description,
			Skills:      wk.Skills,
			ConnectedAt: wk.ConnectedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	tasks := s.swarm.ActiveTasks()
	type taskInfo struct {
		ID       string `json:"id"`
		Skill    string `json:"skill"`
		Prompt   string `json:"prompt"`
		WorkerID string `json:"worker_id"`
		Status   string `json:"status"`
	}

	result := make([]taskInfo, 0, len(tasks))
	for _, t := range tasks {
		result = append(result, taskInfo{
			ID:       t.ID,
			Skill:    t.Skill,
			Prompt:   t.Prompt,
			WorkerID: t.WorkerID,
			Status:   string(t.Status),
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}

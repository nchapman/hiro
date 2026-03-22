// Package api implements the HTTP server that serves the web UI,
// the chat WebSocket endpoint, and the REST API for the dashboard.
package api

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/nchapman/hivebot/internal/agent"
)

// Server is the HTTP server for the Hive leader.
type Server struct {
	manager  *agent.Manager // agent manager (nil = no agents)
	leaderID string         // ID of the leader agent for chat
	webFS    fs.FS          // embedded web UI files (nil = no UI serving)
	mux      *http.ServeMux
	logger   *slog.Logger
}

// NewServer creates a new API server. If webFS is non-nil, the web UI
// will be served for any request that doesn't match an API route.
func NewServer(logger *slog.Logger, webFS fs.FS) *Server {
	s := &Server{
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

	// WebSocket endpoint for web UI chat
	s.mux.HandleFunc("/ws/chat", s.handleChat)

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

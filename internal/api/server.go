// Package api implements the HTTP server that serves the web UI,
// the chat WebSocket endpoint, and the REST API for the dashboard.
package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/nchapman/hivebot/internal/agent"
	"github.com/nchapman/hivebot/internal/controlplane"
	"github.com/nchapman/hivebot/internal/watcher"
)

// CommandHandler handles slash commands from the chat interface.
type CommandHandler interface {
	HandleCommand(input string) (string, error)
}

// Server is the HTTP server for the Hive leader.
type Server struct {
	manager      *agent.Manager          // session manager (nil = no sessions)
	leaderID     string                  // ID of the leader session for chat
	cmdHandler   CommandHandler          // control plane command handler (nil = no commands)
	cp           *controlplane.ControlPlane // control plane (for auth + settings)
	startManager func() error            // callback to start the session manager (set by main)
	webFS        fs.FS                   // embedded web UI files (nil = no UI serving)
	rootDir      string                  // platform root directory (for terminal working dir)
	watcher      *watcher.Watcher        // filesystem watcher for HIVE_ROOT (nil = no watching)
	mux          *http.ServeMux
	logger       *slog.Logger
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
	// Auth routes (unauthenticated)
	s.mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)

	// Auth routes (authenticated)
	s.mux.HandleFunc("POST /api/auth/logout", s.requireAuth(s.handleLogout))
	s.mux.HandleFunc("POST /api/auth/password", s.requireAuth(s.handleChangePassword))

	// Setup routes (unauthenticated, only work when needsSetup is true)
	s.mux.HandleFunc("POST /api/setup", s.handleSetup)
	s.mux.HandleFunc("POST /api/setup/test-provider", s.handleTestProvider)

	// Settings routes (authenticated)
	s.mux.HandleFunc("GET /api/settings", s.requireAuth(s.handleGetSettings))
	s.mux.HandleFunc("PUT /api/settings", s.requireAuth(s.handleUpdateSettings))
	s.mux.HandleFunc("GET /api/settings/providers", s.requireAuth(s.handleListProviders))
	s.mux.HandleFunc("PUT /api/settings/providers/{type}", s.requireAuth(s.handlePutProvider))
	s.mux.HandleFunc("DELETE /api/settings/providers/{type}", s.requireAuth(s.handleDeleteProvider))
	s.mux.HandleFunc("POST /api/settings/providers/{type}/test", s.requireAuth(s.handleTestProviderByType))

	// Session API routes (authenticated)
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/sessions", s.requireAuth(s.handleListSessions))
	s.mux.HandleFunc("GET /api/sessions/{id}/messages", s.requireAuth(s.handleSessionMessages))
	s.mux.HandleFunc("POST /api/sessions/{id}/stop", s.requireAuth(s.handleStopSession))
	s.mux.HandleFunc("POST /api/sessions/{id}/start", s.requireAuth(s.handleStartSession))
	s.mux.HandleFunc("DELETE /api/sessions/{id}", s.requireAuth(s.handleDeleteSession))

	// File browser (authenticated)
	s.mux.HandleFunc("GET /api/files/tree", s.requireAuth(s.handleFilesTree))
	s.mux.HandleFunc("GET /api/files/file", s.requireAuth(s.handleFilesFileRead))
	s.mux.HandleFunc("PUT /api/files/file", s.requireAuth(s.handleFilesFileWrite))
	s.mux.HandleFunc("POST /api/files/mkdir", s.requireAuth(s.handleFilesMkdir))
	s.mux.HandleFunc("DELETE /api/files/file", s.requireAuth(s.handleFilesDelete))
	s.mux.HandleFunc("POST /api/files/rename", s.requireAuth(s.handleFilesRename))

	// Shared file viewer (unauthenticated — token is the access control)
	s.mux.HandleFunc("POST /api/files/share", s.requireAuth(s.handleShareCreate))
	s.mux.HandleFunc("GET /api/shared/{token}", s.handleSharedFileInfo)
	s.mux.HandleFunc("GET /api/shared/{token}/raw", s.handleSharedFileRaw)

	// WebSocket endpoints
	s.mux.HandleFunc("/ws/chat", s.handleChat)
	s.mux.HandleFunc("/ws/terminal", s.requireAuth(s.handleTerminal))

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

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if s.manager == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	modeFilter := r.URL.Query().Get("mode")
	sessions := s.manager.ListSessions()
	type sessionResponse struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Mode        string `json:"mode"`
		Status      string `json:"status"`
		Description string `json:"description,omitempty"`
		ParentID    string `json:"parent_id,omitempty"`
	}
	result := make([]sessionResponse, 0, len(sessions))
	for _, si := range sessions {
		if modeFilter != "" && string(si.Mode) != modeFilter {
			continue
		}
		result = append(result, sessionResponse{
			ID:          si.ID,
			Name:        si.Name,
			Mode:        string(si.Mode),
			Status:      string(si.Status),
			Description: si.Description,
			ParentID:    si.ParentID,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	if s.manager == nil {
		http.Error(w, "no session manager", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	msgs, err := s.manager.GetHistory(id, 100)
	if err != nil {
		if errors.Is(err, agent.ErrSessionNotFound) {
			http.Error(w, "session not found", http.StatusNotFound)
		} else {
			s.logger.Error("failed to read session history", "id", id, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) handleStopSession(w http.ResponseWriter, r *http.Request) {
	if s.manager == nil {
		http.Error(w, "no session manager", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")

	// Protect root session (coordinator).
	info, ok := s.manager.GetSession(id)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if info.ParentID == "" {
		http.Error(w, "cannot stop the root session", http.StatusForbidden)
		return
	}

	if _, err := s.manager.StopSession(id); err != nil {
		s.logger.Error("failed to stop session", "id", id, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStartSession(w http.ResponseWriter, r *http.Request) {
	if s.manager == nil {
		http.Error(w, "no session manager", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")

	// Protect root session (coordinator).
	info, ok := s.manager.GetSession(id)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if info.ParentID == "" {
		http.Error(w, "cannot restart the root session", http.StatusForbidden)
		return
	}

	if err := s.manager.StartSession(r.Context(), id); err != nil {
		if errors.Is(err, agent.ErrSessionNotFound) {
			http.Error(w, "session not found", http.StatusNotFound)
		} else if errors.Is(err, agent.ErrSessionNotStopped) {
			http.Error(w, "session is not stopped", http.StatusConflict)
		} else {
			s.logger.Error("failed to start session", "id", id, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if s.manager == nil {
		http.Error(w, "no session manager", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")

	// Protect root session (coordinator).
	info, ok := s.manager.GetSession(id)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if info.ParentID == "" {
		http.Error(w, "cannot delete the root session", http.StatusForbidden)
		return
	}

	if err := s.manager.DeleteSession(id); err != nil {
		s.logger.Error("failed to delete session", "id", id, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isSameOrigin checks that the request's Origin header (if present) matches
// the server's Host. This provides CSRF protection for unauthenticated
// mutation endpoints (like setup).
func isSameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No Origin header means this is not a cross-origin request (or is
		// a same-origin request from a non-browser client).
		return true
	}
	// Origin includes scheme (e.g. "http://localhost:8080").
	// Extract host and compare with the request's Host header.
	host := r.Host
	if host == "" {
		return false
	}
	// Strip scheme from origin to get the host portion.
	originHost := origin
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(originHost, prefix) {
			originHost = originHost[len(prefix):]
			break
		}
	}
	return originHost == host
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

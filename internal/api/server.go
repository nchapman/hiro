// Package api implements the HTTP server that serves the web UI,
// the chat WebSocket endpoint, and the REST API for the dashboard.
package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/nchapman/hiro/internal/agent"
	"github.com/nchapman/hiro/internal/cluster"
	"github.com/nchapman/hiro/internal/controlplane"
	platformdb "github.com/nchapman/hiro/internal/platform/db"
	"github.com/nchapman/hiro/internal/platform/loghandler"
	"github.com/nchapman/hiro/internal/watcher"
)

// CommandHandler handles slash commands from the chat interface.
type CommandHandler interface {
	HandleCommand(input string) (string, error)
}

// Server is the HTTP server for the Hiro leader.
type Server struct {
	manager      *agent.Manager          // instance manager (nil = no instances)
	leaderID     string                  // ID of the leader instance for chat
	cmdHandler   CommandHandler          // control plane command handler (nil = no commands)
	cp           *controlplane.ControlPlane // control plane (for auth + settings)
	pdb          *platformdb.DB          // platform database (nil = no usage endpoints)
	startManager   func() error            // callback to start the instance manager (set by main)
	startCluster   func() error            // callback to start the cluster gRPC server (set by main)
	requestRestart func()                  // callback to request a process restart (set by main)
	nodeRegistry    *cluster.NodeRegistry   // cluster node registry (leader mode only)
	pendingRegistry *cluster.PendingRegistry // pending node approval registry (leader mode only)
	workerStatus    func() string            // returns worker connection status (worker mode only)
	disconnectNode  func(string)             // forcefully disconnect a node (leader mode only)
	webFS          fs.FS                   // embedded web UI files (nil = no UI serving)
	rootDir      string                  // platform root directory (for terminal working dir)
	watcher      *watcher.Watcher        // filesystem watcher for HIRO_ROOT (nil = no watching)
	logHandler   *loghandler.Handler     // log handler for real-time streaming (nil = no log SSE)
	limiter      *loginLimiter           // login rate limiter (per-server for testability)
	mux          *http.ServeMux
	logger       *slog.Logger
}

// NewServer creates a new API server with its required dependencies.
// cp and pdb may be nil for tests that don't need them.
// webFS controls whether the web UI is served for non-API requests.
func NewServer(logger *slog.Logger, webFS fs.FS, cp *controlplane.ControlPlane, pdb *platformdb.DB, rootDir string) *Server {
	s := &Server{
		webFS:      webFS,
		cp:         cp,
		cmdHandler: cp, // ControlPlane implements CommandHandler
		pdb:        pdb,
		rootDir:    rootDir,
		limiter:    defaultLimiter,
		mux:        http.NewServeMux(),
		logger:     logger.With("component", "api"),
	}
	s.routes()
	return s
}

// hasManager reports whether the agent manager has been initialized.
func (s *Server) hasManager() bool { return s.manager != nil }

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
	s.mux.HandleFunc("POST /api/setup/validate-swarm", s.handleValidateSwarm)
	s.mux.HandleFunc("GET /api/setup/provider-types", s.handleListProviderTypes)
	s.mux.HandleFunc("GET /api/setup/models", s.handleListModels)

	// Settings routes (authenticated)
	s.mux.HandleFunc("GET /api/settings", s.requireAuth(s.handleGetSettings))
	s.mux.HandleFunc("PUT /api/settings", s.requireAuth(s.handleUpdateSettings))
	s.mux.HandleFunc("GET /api/settings/cluster", s.requireAuth(s.handleGetClusterSettings))
	s.mux.HandleFunc("POST /api/settings/cluster/reset", s.requireStrictAuth(s.handleClusterReset))
	s.mux.HandleFunc("GET /api/cluster/pending", s.requireStrictAuth(s.handleListPending))
	s.mux.HandleFunc("POST /api/cluster/pending/{nodeID}/approve", s.requireStrictAuth(s.handleApproveNode))
	s.mux.HandleFunc("DELETE /api/cluster/pending/{nodeID}", s.requireStrictAuth(s.handleDismissNode))
	s.mux.HandleFunc("GET /api/cluster/approved", s.requireStrictAuth(s.handleListApproved))
	s.mux.HandleFunc("DELETE /api/cluster/approved/{nodeID}", s.requireStrictAuth(s.handleRemoveApproved))
	s.mux.HandleFunc("GET /api/settings/providers", s.requireAuth(s.handleListProviders))
	s.mux.HandleFunc("PUT /api/settings/providers/{type}", s.requireAuth(s.handlePutProvider))
	s.mux.HandleFunc("DELETE /api/settings/providers/{type}", s.requireAuth(s.handleDeleteProvider))
	s.mux.HandleFunc("POST /api/settings/providers/{type}/test", s.requireAuth(s.handleTestProviderByType))

	// Instance API routes (authenticated)
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/instances", s.requireAuth(s.handleListInstances))
	s.mux.HandleFunc("GET /api/instances/{id}/messages", s.requireAuth(s.handleInstanceMessages))
	s.mux.HandleFunc("POST /api/instances/{id}/stop", s.requireAuth(s.handleStopInstance))
	s.mux.HandleFunc("POST /api/instances/{id}/start", s.requireAuth(s.handleStartInstance))
	s.mux.HandleFunc("POST /api/instances/{id}/clear", s.requireAuth(s.handleClearInstance))
	s.mux.HandleFunc("DELETE /api/instances/{id}", s.requireAuth(s.handleDeleteInstance))
	s.mux.HandleFunc("GET /api/sessions/{id}/messages", s.requireAuth(s.handleSessionMessages))

	// Models & provider types API (authenticated)
	s.mux.HandleFunc("GET /api/models", s.requireAuth(s.handleListModels))
	s.mux.HandleFunc("GET /api/provider-types", s.requireAuth(s.handleListProviderTypes))

	// Usage API routes (authenticated)
	s.mux.HandleFunc("GET /api/instances/{id}/usage", s.requireAuth(s.handleInstanceUsage))
	s.mux.HandleFunc("GET /api/usage", s.requireAuth(s.handleTotalUsage))
	s.mux.HandleFunc("GET /api/usage/models", s.requireAuth(s.handleUsageByModel))
	s.mux.HandleFunc("GET /api/usage/daily", s.requireAuth(s.handleUsageByDay))

	// Logs API routes (strict auth — never accessible during setup)
	s.mux.HandleFunc("GET /api/logs", s.requireStrictAuth(s.handleQueryLogs))
	s.mux.HandleFunc("GET /api/logs/stream", s.requireStrictAuth(s.handleLogStream))
	s.mux.HandleFunc("GET /api/logs/sources", s.requireStrictAuth(s.handleLogSources))

	// File browser (authenticated)
	s.mux.HandleFunc("GET /api/files/tree", s.requireAuth(s.handleFilesTree))
	s.mux.HandleFunc("GET /api/files/file", s.requireAuth(s.handleFilesFileRead))
	s.mux.HandleFunc("PUT /api/files/file", s.requireAuth(s.handleFilesFileWrite))
	s.mux.HandleFunc("POST /api/files/mkdir", s.requireAuth(s.handleFilesMkdir))
	s.mux.HandleFunc("DELETE /api/files/file", s.requireAuth(s.handleFilesDelete))
	s.mux.HandleFunc("POST /api/files/rename", s.requireAuth(s.handleFilesRename))
	s.mux.HandleFunc("GET /api/files/events", s.requireAuth(s.handleFilesWatch))

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
	resp := map[string]string{"status": "ok"}
	if s.cp != nil {
		mode := s.cp.ClusterMode()
		if mode != "" {
			resp["mode"] = mode
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleListInstances(w http.ResponseWriter, r *http.Request) {
	if !s.hasManager() {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	modeFilter := r.URL.Query().Get("mode")
	instances := s.manager.ListInstances()
	type instanceResponse struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Mode        string `json:"mode"`
		Status      string `json:"status"`
		Description string `json:"description,omitempty"`
		ParentID    string `json:"parent_id,omitempty"`
		Model       string `json:"model,omitempty"`
	}
	result := make([]instanceResponse, 0, len(instances))
	for _, inst := range instances {
		if modeFilter != "" && string(inst.Mode) != modeFilter {
			continue
		}
		result = append(result, instanceResponse{
			ID:          inst.ID,
			Name:        inst.Name,
			Mode:        string(inst.Mode),
			Status:      string(inst.Status),
			Description: inst.Description,
			ParentID:    inst.ParentID,
			Model:       inst.Model,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleInstanceMessages(w http.ResponseWriter, r *http.Request) {
	if !s.hasManager() {
		http.Error(w, "no instance manager", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	msgs, err := s.manager.GetHistory(id, 100)
	if err != nil {
		if errors.Is(err, agent.ErrInstanceNotFound) {
			http.Error(w, "instance not found", http.StatusNotFound)
		} else {
			s.logger.Error("failed to read instance history", "id", id, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	if !s.hasManager() {
		http.Error(w, "no instance manager", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	msgs, err := s.manager.GetSessionHistory(id, 100)
	if err != nil {
		s.logger.Error("failed to read session history", "id", id, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) handleStopInstance(w http.ResponseWriter, r *http.Request) {
	if !s.hasManager() {
		http.Error(w, "no instance manager", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")

	// Protect root instance (coordinator).
	info, ok := s.manager.GetInstance(id)
	if !ok {
		http.Error(w, "instance not found", http.StatusNotFound)
		return
	}
	if info.ParentID == "" {
		http.Error(w, "cannot stop the root instance", http.StatusForbidden)
		return
	}

	if _, err := s.manager.StopInstance(id); err != nil {
		s.logger.Error("failed to stop instance", "id", id, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStartInstance(w http.ResponseWriter, r *http.Request) {
	if !s.hasManager() {
		http.Error(w, "no instance manager", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")

	info, ok := s.manager.GetInstance(id)
	if !ok {
		http.Error(w, "instance not found", http.StatusNotFound)
		return
	}
	if info.ParentID == "" {
		http.Error(w, "cannot restart the root instance", http.StatusForbidden)
		return
	}

	if err := s.manager.StartInstance(r.Context(), id); err != nil {
		if errors.Is(err, agent.ErrInstanceNotFound) {
			http.Error(w, "instance not found", http.StatusNotFound)
		} else if errors.Is(err, agent.ErrInstanceNotStopped) {
			http.Error(w, "instance is not stopped", http.StatusConflict)
		} else {
			s.logger.Error("failed to start instance", "id", id, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleClearInstance(w http.ResponseWriter, r *http.Request) {
	if !s.hasManager() {
		http.Error(w, "no instance manager", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")

	newSessionID, err := s.manager.NewSession(id)
	if err != nil {
		if errors.Is(err, agent.ErrInstanceNotFound) {
			http.Error(w, "instance not found", http.StatusNotFound)
		} else {
			s.logger.Error("failed to clear instance", "id", id, "error", err)
			http.Error(w, err.Error(), http.StatusConflict)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"new_session_id": newSessionID})
}

func (s *Server) handleDeleteInstance(w http.ResponseWriter, r *http.Request) {
	if !s.hasManager() {
		http.Error(w, "no instance manager", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")

	// Protect root instance (coordinator).
	info, ok := s.manager.GetInstance(id)
	if !ok {
		http.Error(w, "instance not found", http.StatusNotFound)
		return
	}
	if info.ParentID == "" {
		http.Error(w, "cannot delete the root instance", http.StatusForbidden)
		return
	}

	if err := s.manager.DeleteInstance(id); err != nil {
		s.logger.Error("failed to delete instance", "id", id, "error", err)
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

// isLoopbackOrigin hardens setup endpoints against DNS rebinding attacks.
// If an Origin header is present (browser request), it must match the Host
// header AND the host must be a loopback address. Without an Origin header
// (non-browser clients like curl), the request is allowed since DNS
// rebinding requires a browser.
func isLoopbackOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No Origin → not a browser cross-origin request → no DNS rebinding risk.
		return true
	}
	if !isSameOrigin(r) {
		return false
	}
	// Browser request with matching Origin/Host — verify the host is loopback.
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
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

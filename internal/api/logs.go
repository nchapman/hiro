package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/nchapman/hivebot/internal/platform/db"
	"github.com/nchapman/hivebot/internal/platform/loghandler"
)

// SetLogHandler sets the log handler for real-time log streaming.
func (s *Server) SetLogHandler(lh *loghandler.Handler) {
	s.logHandler = lh
}

// validLogLevels is the set of accepted level filter values.
var validLogLevels = map[string]bool{
	"DEBUG": true,
	"INFO":  true,
	"WARN":  true,
	"ERROR": true,
}

// handleQueryLogs returns paginated log entries matching the given filters.
func (s *Server) handleQueryLogs(w http.ResponseWriter, r *http.Request) {
	if s.pdb == nil {
		http.Error(w, "logging unavailable", http.StatusServiceUnavailable)
		return
	}

	const maxFilterLen = 256

	q := r.URL.Query()
	var opts db.LogQuery

	if v := q.Get("level"); v != "" {
		if !validLogLevels[v] {
			http.Error(w, "invalid level: must be DEBUG, INFO, WARN, or ERROR", http.StatusBadRequest)
			return
		}
		opts.Level = v
	}
	if v := q.Get("component"); v != "" {
		if len(v) > maxFilterLen {
			http.Error(w, "component filter too long", http.StatusBadRequest)
			return
		}
		opts.Component = v
	}
	if v := q.Get("search"); v != "" {
		if len(v) > maxFilterLen {
			http.Error(w, "search filter too long", http.StatusBadRequest)
			return
		}
		opts.Search = v
	}

	if v := q.Get("before"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			opts.Before = n
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.Limit = n
		}
	}

	logs, err := s.pdb.QueryLogs(opts)
	if err != nil {
		s.logger.Error("failed to query logs", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if logs == nil {
		logs = []db.LogEntry{}
	}

	writeJSON(w, http.StatusOK, logs)
}

// handleLogStream streams log entries in real-time via Server-Sent Events.
func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	if s.logHandler == nil {
		http.Error(w, "log streaming not available", http.StatusServiceUnavailable)
		return
	}

	// Set headers before subscribing so the response isn't committed
	// if Subscribe fails (avoids swallowing the 503).
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	ch, unsub, err := s.logHandler.Subscribe()
	if err != nil {
		// Headers not yet flushed, so we can still send a proper error.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.Error(w, "too many log stream connections", http.StatusServiceUnavailable)
		return
	}
	defer unsub()

	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case entry := <-ch:
			data, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleLogSources returns the distinct component names from the logs table.
func (s *Server) handleLogSources(w http.ResponseWriter, _ *http.Request) {
	if s.pdb == nil {
		http.Error(w, "logging unavailable", http.StatusServiceUnavailable)
		return
	}

	sources, err := s.pdb.LogSources()
	if err != nil {
		s.logger.Error("failed to get log sources", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if sources == nil {
		sources = []string{}
	}

	writeJSON(w, http.StatusOK, sources)
}

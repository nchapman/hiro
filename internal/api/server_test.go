package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nchapman/hivebot/internal/hub"
)

func newTestServer() (*Server, *hub.Swarm) {
	swarm := hub.NewSwarm("test-swarm")
	logger := slog.Default()
	return NewServer(swarm, logger, nil), swarm
}

func TestHealthEndpoint(t *testing.T) {
	srv, _ := newTestServer()

	req := httptest.NewRequest("GET", "/api/health", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want %q", body["status"], "ok")
	}
}

func TestSwarmStatusEndpoint(t *testing.T) {
	srv, swarm := newTestServer()
	swarm.AddWorker(&hub.Worker{
		ID:        "w1",
		AgentName: "test",
		Skills:    []string{"search"},
	})

	req := httptest.NewRequest("GET", "/api/swarm", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["swarm_code"] != "test-swarm" {
		t.Errorf("swarm_code = %v, want %q", body["swarm_code"], "test-swarm")
	}
	if body["worker_count"].(float64) != 1 {
		t.Errorf("worker_count = %v, want 1", body["worker_count"])
	}
}

func TestListWorkersEndpoint(t *testing.T) {
	srv, swarm := newTestServer()
	swarm.AddWorker(&hub.Worker{
		ID:          "w1",
		AgentName:   "researcher",
		Description: "Research agent",
		Skills:      []string{"search", "summarize"},
		ConnectedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})

	req := httptest.NewRequest("GET", "/api/workers", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 {
		t.Fatalf("expected 1 worker, got %d", len(body))
	}
	if body[0]["agent_name"] != "researcher" {
		t.Errorf("agent_name = %v, want %q", body[0]["agent_name"], "researcher")
	}
}

func TestListTasksEndpoint(t *testing.T) {
	srv, swarm := newTestServer()
	swarm.AddTask(&hub.Task{
		ID:     "t1",
		Skill:  "search",
		Prompt: "find Go docs",
		Status: hub.TaskAssigned,
	})

	req := httptest.NewRequest("GET", "/api/tasks", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

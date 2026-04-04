package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nchapman/hiro/internal/agent"
	"github.com/nchapman/hiro/internal/ipc"
	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

// testWorkerFactory returns a fake worker that does nothing.
func testWorkerFactory(_ context.Context, _ ipc.SpawnConfig) (*agent.WorkerHandle, error) {
	done := make(chan struct{})
	closed := make(chan struct{})
	fw := &fakeWorker{done: done}
	return &agent.WorkerHandle{
		Worker: fw,
		Kill:   func() { fw.shutdown() },
		Close: func() {
			select {
			case <-closed:
			default:
				close(closed)
			}
		},
		Done: done,
	}, nil
}

type fakeWorker struct {
	done chan struct{}
	once sync.Once
}

func (f *fakeWorker) shutdown() {
	f.once.Do(func() { close(f.done) })
}

func (f *fakeWorker) ExecuteTool(_ context.Context, _, _, _ string) (ipc.ToolResult, error) {
	return ipc.ToolResult{Content: "ok"}, nil
}
func (f *fakeWorker) Shutdown(_ context.Context) error {
	f.shutdown()
	return nil
}

// newInstanceTestServer creates a server with a real manager backed by a temp directory.
// Returns an agent name that has a valid definition (can create instances from it).
func newInstanceTestServer(t *testing.T) (*Server, *agent.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Create a minimal agent definition.
	agentDir := filepath.Join(dir, "agents", "test-agent")
	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: test-agent\n---\nYou are a test agent.\n"), 0o644)

	// Create instances directory.
	os.MkdirAll(filepath.Join(dir, "instances"), 0o755)

	pdb, _ := platformdb.Open(filepath.Join(dir, "test.db"))
	t.Cleanup(func() { pdb.Close() })

	mgr := agent.NewManager(
		context.Background(), dir, agent.Options{WorkingDir: dir},
		nil, logger, testWorkerFactory, nil, pdb,
	)
	t.Cleanup(func() { mgr.Shutdown() })

	srv := NewServer(logger, nil, nil, pdb, "")
	srv.manager = mgr
	return srv, mgr, "test-agent"
}

func TestListInstances_Empty(t *testing.T) {
	srv, _, _ := newInstanceTestServer(t)

	req := httptest.NewRequest("GET", "/api/instances", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	var result []json.RawMessage
	json.NewDecoder(rec.Body).Decode(&result)
	if len(result) != 0 {
		t.Errorf("expected empty list, got %d instances", len(result))
	}
}

func TestListInstances_NoManager(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/instances", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 (empty list)", rec.Code)
	}
}

func TestListInstances_WithInstances(t *testing.T) {
	srv, mgr, agentName := newInstanceTestServer(t)

	// Create an instance.
	_, err := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/instances", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	var result []map[string]any
	json.NewDecoder(rec.Body).Decode(&result)
	if len(result) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(result))
	}
	if result[0]["name"] != agentName {
		t.Errorf("name=%v, want %s", result[0]["name"], agentName)
	}
	if result[0]["mode"] != "persistent" {
		t.Errorf("mode=%v, want persistent", result[0]["mode"])
	}
}

func TestListInstances_ModeFilter(t *testing.T) {
	srv, mgr, agentName := newInstanceTestServer(t)

	_, err := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	// Filter by ephemeral → should be empty.
	req := httptest.NewRequest("GET", "/api/instances?mode=ephemeral", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	var result []map[string]any
	json.NewDecoder(rec.Body).Decode(&result)
	if len(result) != 0 {
		t.Errorf("expected 0 ephemeral instances, got %d", len(result))
	}
}

func TestStopInstance_NotFound(t *testing.T) {
	srv, _, _ := newInstanceTestServer(t)

	req := httptest.NewRequest("POST", "/api/instances/nonexistent/stop", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rec.Code)
	}
}

func TestStopInstance_ForbidsRoot(t *testing.T) {
	srv, mgr, agentName := newInstanceTestServer(t)

	// Create a root instance (no parent).
	id, _ := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")

	req := httptest.NewRequest("POST", "/api/instances/"+id+"/stop", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 for root instance", rec.Code)
	}
}

func TestDeleteInstance_ForbidsRoot(t *testing.T) {
	srv, mgr, agentName := newInstanceTestServer(t)

	id, _ := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")

	req := httptest.NewRequest("DELETE", "/api/instances/"+id, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 for root instance", rec.Code)
	}
}

func TestInstanceMessages_NotFound(t *testing.T) {
	srv, _, _ := newInstanceTestServer(t)

	req := httptest.NewRequest("GET", "/api/instances/nonexistent/messages", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rec.Code)
	}
}

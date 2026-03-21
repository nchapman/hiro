package transport

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nchapman/hivebot/internal/hub"
)

func TestWorkerRegistration(t *testing.T) {
	swarm := hub.NewSwarm("test-swarm")
	logger := slog.Default()
	srv := NewServer(swarm, logger)

	// Start HTTP test server
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/worker", srv.HandleWebSocket)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/worker"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := NewClient(ClientOptions{
		LeaderURL:   wsURL,
		AgentName:   "test-worker",
		Description: "A test worker",
		Skills:      []string{"search", "summarize"},
		SwarmCode:   "test-swarm",
		Handler: func(ctx context.Context, skill, prompt, taskContext string) (string, error) {
			return "result", nil
		},
		Logger: logger,
	})

	// Connect in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Connect(ctx)
	}()

	// Wait for worker to appear in swarm
	deadline := time.After(3 * time.Second)
	for {
		workers := swarm.Workers()
		if len(workers) == 1 {
			w := workers[0]
			if w.AgentName != "test-worker" {
				t.Errorf("agent name = %q, want %q", w.AgentName, "test-worker")
			}
			if len(w.Skills) != 2 || w.Skills[0] != "search" {
				t.Errorf("skills = %v, want [search summarize]", w.Skills)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("worker did not register within timeout")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	cancel()
}

func TestWorkerRegistration_BadSwarmCode(t *testing.T) {
	swarm := hub.NewSwarm("test-swarm")
	logger := slog.Default()
	srv := NewServer(swarm, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/worker", srv.HandleWebSocket)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/worker"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client := NewClient(ClientOptions{
		LeaderURL:   wsURL,
		AgentName:   "bad-worker",
		Skills:      []string{"search"},
		SwarmCode:   "wrong-code",
		Handler:     func(ctx context.Context, skill, prompt, taskContext string) (string, error) { return "", nil },
		Logger:      logger,
	})

	err := client.Connect(ctx)
	if err == nil {
		t.Fatal("expected error for bad swarm code")
	}
}

func TestTaskDelegation(t *testing.T) {
	swarm := hub.NewSwarm("test-swarm")
	logger := slog.Default()
	srv := NewServer(swarm, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/worker", srv.HandleWebSocket)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/worker"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Worker that echoes back the prompt
	client := NewClient(ClientOptions{
		LeaderURL:   wsURL,
		AgentName:   "echo-worker",
		Description: "Echoes tasks",
		Skills:      []string{"echo"},
		SwarmCode:   "test-swarm",
		Handler: func(ctx context.Context, skill, prompt, taskContext string) (string, error) {
			return fmt.Sprintf("echo: %s", prompt), nil
		},
		Logger: logger,
	})

	go client.Connect(ctx)

	// Wait for registration
	var worker hub.Worker
	deadline := time.After(3 * time.Second)
	for {
		workers := swarm.FindWorkers("echo")
		if len(workers) > 0 {
			worker = workers[0]
			break
		}
		select {
		case <-deadline:
			t.Fatal("worker did not register")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Dispatch a task
	result, err := srv.DispatchTask(ctx, worker, "echo", "hello world", "")
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	expected := "echo: hello world"
	if result != expected {
		t.Errorf("result = %q, want %q", result, expected)
	}

	// Verify task was tracked
	tasks := swarm.ActiveTasks()
	// Task should be completed (not active)
	if len(tasks) != 0 {
		t.Errorf("expected 0 active tasks after completion, got %d", len(tasks))
	}
}

func TestTaskDelegation_WorkerError(t *testing.T) {
	swarm := hub.NewSwarm("test-swarm")
	logger := slog.Default()
	srv := NewServer(swarm, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/worker", srv.HandleWebSocket)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/worker"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := NewClient(ClientOptions{
		LeaderURL: wsURL,
		AgentName: "failing-worker",
		Skills:    []string{"fail"},
		SwarmCode: "test-swarm",
		Handler: func(ctx context.Context, skill, prompt, taskContext string) (string, error) {
			return "", fmt.Errorf("something went wrong")
		},
		Logger: logger,
	})

	go client.Connect(ctx)

	// Wait for registration
	deadline := time.After(3 * time.Second)
	var worker hub.Worker
	for {
		workers := swarm.FindWorkers("fail")
		if len(workers) > 0 {
			worker = workers[0]
			break
		}
		select {
		case <-deadline:
			t.Fatal("worker did not register")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	_, err := srv.DispatchTask(ctx, worker, "fail", "do something", "")
	if err == nil {
		t.Fatal("expected error from failing worker")
	}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Errorf("error = %q, want to contain 'something went wrong'", err.Error())
	}
}

func TestTaskDelegation_WorkerDisconnectMidTask(t *testing.T) {
	swarm := hub.NewSwarm("test-swarm")
	logger := slog.Default()
	srv := NewServer(swarm, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/worker", srv.HandleWebSocket)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/worker"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Worker that blocks forever on the task (simulating a slow task)
	workerCtx, workerCancel := context.WithCancel(ctx)
	client := NewClient(ClientOptions{
		LeaderURL: wsURL,
		AgentName: "slow-worker",
		Skills:    []string{"slow"},
		SwarmCode: "test-swarm",
		Handler: func(ctx context.Context, skill, prompt, taskContext string) (string, error) {
			// Block until context is cancelled (worker disconnect)
			<-ctx.Done()
			return "", ctx.Err()
		},
		Logger: logger,
	})

	go client.Connect(workerCtx)

	// Wait for registration
	var worker hub.Worker
	deadline := time.After(3 * time.Second)
	for {
		workers := swarm.FindWorkers("slow")
		if len(workers) > 0 {
			worker = workers[0]
			break
		}
		select {
		case <-deadline:
			t.Fatal("worker did not register")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Dispatch task in background, then kill the worker
	errCh := make(chan error, 1)
	go func() {
		_, err := srv.DispatchTask(ctx, worker, "slow", "do something slow", "")
		errCh <- err
	}()

	// Give the task time to be dispatched, then disconnect the worker
	time.Sleep(200 * time.Millisecond)
	workerCancel()

	// The dispatch should fail with "worker disconnected"
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error when worker disconnects mid-task")
		}
		if !strings.Contains(err.Error(), "worker disconnected") {
			t.Errorf("error = %q, want to contain 'worker disconnected'", err.Error())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DispatchTask did not return after worker disconnect — pending channel leak")
	}
}

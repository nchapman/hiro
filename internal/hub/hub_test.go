package hub

import (
	"testing"
	"time"
)

func TestSwarm_WorkerLifecycle(t *testing.T) {
	s := NewSwarm("test-swarm")

	if s.Code() != "test-swarm" {
		t.Errorf("code = %q, want %q", s.Code(), "test-swarm")
	}

	w := &Worker{
		ID:          "w1",
		AgentName:   "researcher",
		Skills:      []string{"search", "summarize"},
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
	}
	s.AddWorker(w)

	if _, ok := s.GetWorker("w1"); !ok {
		t.Fatal("expected worker w1")
	}
	if _, ok := s.GetWorker("nonexistent"); ok {
		t.Fatal("expected not found for nonexistent worker")
	}

	workers := s.Workers()
	if len(workers) != 1 {
		t.Fatalf("expected 1 worker, got %d", len(workers))
	}

	s.RemoveWorker("w1")
	if _, ok := s.GetWorker("w1"); ok {
		t.Fatal("expected not found after removal")
	}
}

func TestSwarm_FindWorkers(t *testing.T) {
	s := NewSwarm("test")

	s.AddWorker(&Worker{ID: "w1", Skills: []string{"search", "summarize"}})
	s.AddWorker(&Worker{ID: "w2", Skills: []string{"code-review", "search"}})
	s.AddWorker(&Worker{ID: "w3", Skills: []string{"code-review"}})

	results := s.FindWorkers("search")
	if len(results) != 2 {
		t.Fatalf("expected 2 workers with 'search', got %d", len(results))
	}

	results = s.FindWorkers("code-review")
	if len(results) != 2 {
		t.Fatalf("expected 2 workers with 'code-review', got %d", len(results))
	}

	results = s.FindWorkers("nonexistent")
	if len(results) != 0 {
		t.Fatalf("expected 0 workers with 'nonexistent', got %d", len(results))
	}
}

func TestWorker_HasSkill(t *testing.T) {
	w := Worker{Skills: []string{"a", "b", "c"}}
	if !w.HasSkill("b") {
		t.Error("expected HasSkill(b) = true")
	}
	if w.HasSkill("d") {
		t.Error("expected HasSkill(d) = false")
	}
}

func TestSwarm_TaskLifecycle(t *testing.T) {
	s := NewSwarm("test")

	task := &Task{
		ID:        "t1",
		Skill:     "search",
		Prompt:    "find info about Go",
		WorkerID:  "w1",
		Status:    TaskAssigned,
		CreatedAt: time.Now(),
	}
	s.AddTask(task)

	got, ok := s.GetTask("t1")
	if !ok {
		t.Fatal("expected task t1")
	}
	if got.Status != TaskAssigned {
		t.Errorf("status = %q, want %q", got.Status, TaskAssigned)
	}

	active := s.ActiveTasks()
	if len(active) != 1 {
		t.Fatalf("expected 1 active task, got %d", len(active))
	}

	if err := s.CompleteTask("t1", "found the info"); err != nil {
		t.Fatal(err)
	}

	got, ok = s.GetTask("t1")
	if !ok {
		t.Fatal("expected task t1 after completion")
	}
	if got.Status != TaskCompleted {
		t.Errorf("status = %q, want %q", got.Status, TaskCompleted)
	}
	if got.Result != "found the info" {
		t.Errorf("result = %q, want %q", got.Result, "found the info")
	}

	active = s.ActiveTasks()
	if len(active) != 0 {
		t.Fatalf("expected 0 active tasks, got %d", len(active))
	}
}

func TestSwarm_FailTask(t *testing.T) {
	s := NewSwarm("test")
	s.AddTask(&Task{ID: "t1", Status: TaskAssigned})

	if err := s.FailTask("t1", "timeout"); err != nil {
		t.Fatal(err)
	}
	got, ok := s.GetTask("t1")
	if !ok {
		t.Fatal("expected task t1")
	}
	if got.Status != TaskFailed {
		t.Errorf("status = %q, want %q", got.Status, TaskFailed)
	}
	if got.Error != "timeout" {
		t.Errorf("error = %q, want %q", got.Error, "timeout")
	}
}

func TestSwarm_CompleteTask_NotFound(t *testing.T) {
	s := NewSwarm("test")
	if err := s.CompleteTask("nonexistent", "result"); err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestSwarm_FailTask_NotFound(t *testing.T) {
	s := NewSwarm("test")
	if err := s.FailTask("nonexistent", "error"); err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestSwarm_GetTask_NotFound(t *testing.T) {
	s := NewSwarm("test")
	_, ok := s.GetTask("nonexistent")
	if ok {
		t.Fatal("expected not found")
	}
}

// Package hub implements the leader-side swarm management: tracking connected
// workers, their skills, and dispatching tasks.
package hub

import (
	"fmt"
	"sync"
	"time"
)

// Worker represents a connected worker agent.
type Worker struct {
	ID          string
	AgentName   string
	Description string
	Skills      []string
	ConnectedAt time.Time
	LastSeen    time.Time
}

// clone returns a deep copy of the worker, including the Skills slice.
func (w *Worker) clone() Worker {
	c := *w
	c.Skills = make([]string, len(w.Skills))
	copy(c.Skills, w.Skills)
	return c
}

// HasSkill reports whether the worker advertises the given skill.
func (w Worker) HasSkill(skill string) bool {
	for _, s := range w.Skills {
		if s == skill {
			return true
		}
	}
	return false
}

// Task represents an in-flight delegated task.
type Task struct {
	ID        string
	Skill     string
	Prompt    string
	WorkerID  string
	Status    TaskStatus
	Result    string
	Error     string
	CreatedAt time.Time
	DoneAt    time.Time
}

// TaskStatus tracks the lifecycle of a task.
type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskAssigned   TaskStatus = "assigned"
	TaskInProgress TaskStatus = "in_progress"
	TaskCompleted  TaskStatus = "completed"
	TaskFailed     TaskStatus = "failed"
)

// Swarm manages the set of connected workers and in-flight tasks.
type Swarm struct {
	mu      sync.RWMutex
	code    string
	workers map[string]*Worker
	tasks   map[string]*Task
}

// NewSwarm creates a new swarm with the given code.
func NewSwarm(code string) *Swarm {
	return &Swarm{
		code:    code,
		workers: make(map[string]*Worker),
		tasks:   make(map[string]*Task),
	}
}

// Code returns the swarm's join code.
func (s *Swarm) Code() string {
	return s.code
}

// AddWorker registers a new worker in the swarm.
func (s *Swarm) AddWorker(w *Worker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workers[w.ID] = w
}

// RemoveWorker removes a worker from the swarm.
func (s *Swarm) RemoveWorker(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.workers, id)
}

// GetWorker returns a copy of a worker by ID. Returns false if not found.
func (s *Swarm) GetWorker(id string) (Worker, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w, ok := s.workers[id]
	if !ok {
		return Worker{}, false
	}
	return w.clone(), true
}

// Workers returns a deep-copy snapshot of all connected workers.
func (s *Swarm) Workers() []Worker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Worker, 0, len(s.workers))
	for _, w := range s.workers {
		result = append(result, w.clone())
	}
	return result
}

// FindWorkers returns deep copies of all workers that have the given skill.
func (s *Swarm) FindWorkers(skill string) []Worker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []Worker
	for _, w := range s.workers {
		if w.HasSkill(skill) {
			result = append(result, w.clone())
		}
	}
	return result
}

// AddTask tracks a new in-flight task.
func (s *Swarm) AddTask(t *Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[t.ID] = t
}

// GetTask returns a copy of a task by ID. Returns false if not found.
func (s *Swarm) GetTask(id string) (Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, false
	}
	return *t, true
}

// CompleteTask marks a task as completed with a result and removes it
// from the active task map.
func (s *Swarm) CompleteTask(id, result string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return fmt.Errorf("task %s not found", id)
	}
	t.Status = TaskCompleted
	t.Result = result
	t.DoneAt = time.Now()
	delete(s.tasks, id)
	return nil
}

// FailTask marks a task as failed with an error and removes it
// from the active task map.
func (s *Swarm) FailTask(id, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return fmt.Errorf("task %s not found", id)
	}
	t.Status = TaskFailed
	t.Error = errMsg
	t.DoneAt = time.Now()
	delete(s.tasks, id)
	return nil
}

// ActiveTasks returns snapshot copies of all tasks not yet completed or failed.
func (s *Swarm) ActiveTasks() []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []Task
	for _, t := range s.tasks {
		if t.Status != TaskCompleted && t.Status != TaskFailed {
			result = append(result, *t)
		}
	}
	return result
}

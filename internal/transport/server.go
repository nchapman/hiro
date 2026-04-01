package transport

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"

	"github.com/nchapman/hiro/internal/hub"
)

const (
	maxAgentNameLen     = 64
	maxDescriptionLen   = 256
	maxSkills           = 20
	maxSkillNameLen     = 64
	maxTaskResultLen    = 32768 // 32KB cap on task results sent to LLM
	registrationTimeout = 10 * time.Second
)

// Server handles WebSocket connections from worker agents.
type Server struct {
	swarm  *hub.Swarm
	logger *slog.Logger

	mu      sync.Mutex
	conns   map[string]*workerConn         // worker ID -> connection
	pending map[string]chan taskResponse   // task ID -> result channel
	tasks   map[string]map[string]struct{} // worker ID -> set of pending task IDs
}

type workerConn struct {
	workerID string
	conn     *websocket.Conn
	writeMu  sync.Mutex // serializes all writes to conn
	cancel   context.CancelFunc
}

// write serializes writes to the worker WebSocket connection.
func (wc *workerConn) write(ctx context.Context, env Envelope) error {
	wc.writeMu.Lock()
	defer wc.writeMu.Unlock()
	return wsjson.Write(ctx, wc.conn, env)
}

type taskResponse struct {
	result string
	err    error
}

// NewServer creates a new WebSocket transport server.
func NewServer(swarm *hub.Swarm, logger *slog.Logger) *Server {
	return &Server{
		swarm:   swarm,
		logger:  logger,
		conns:   make(map[string]*workerConn),
		pending: make(map[string]chan taskResponse),
		tasks:   make(map[string]map[string]struct{}),
	}
}

// HandleWebSocket upgrades connections to WebSocket and manages the worker lifecycle.
func (s *Server) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.logger.Error("websocket accept failed", "error", err)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	s.handleWorkerConnection(ctx, conn, cancel)
}

func (s *Server) handleWorkerConnection(ctx context.Context, conn *websocket.Conn, cancel context.CancelFunc) {
	defer conn.CloseNow()

	// First message must be a register message — enforce a deadline
	regCtx, regCancel := context.WithTimeout(ctx, registrationTimeout)
	defer regCancel()

	var env Envelope
	if err := wsjson.Read(regCtx, conn, &env); err != nil {
		s.logger.Error("failed to read register message", "error", err)
		return
	}

	if env.Type != TypeRegister {
		s.logger.Error("expected register message", "got", env.Type)
		conn.Close(websocket.StatusPolicyViolation, "first message must be register")
		return
	}

	// Parse register payload
	payloadBytes, err := json.Marshal(env.Payload)
	if err != nil {
		s.logger.Error("failed to marshal register payload", "error", err)
		return
	}
	var reg RegisterPayload
	if err := json.Unmarshal(payloadBytes, &reg); err != nil {
		s.logger.Error("failed to parse register payload", "error", err)
		return
	}

	// Validate swarm code — constant-time comparison
	if subtle.ConstantTimeCompare([]byte(reg.SwarmCode), []byte(s.swarm.Code())) != 1 {
		s.logger.Warn("invalid swarm code attempt", "agent", reg.AgentName)
		conn.Close(websocket.StatusPolicyViolation, "invalid swarm code")
		return
	}

	// Validate registration fields
	if reg.AgentName == "" || len(reg.AgentName) > maxAgentNameLen {
		conn.Close(websocket.StatusPolicyViolation, "invalid agent name")
		return
	}
	if len(reg.Description) > maxDescriptionLen {
		conn.Close(websocket.StatusPolicyViolation, "description too long")
		return
	}
	if len(reg.Skills) > maxSkills {
		conn.Close(websocket.StatusPolicyViolation, "too many skills")
		return
	}
	for _, skill := range reg.Skills {
		if skill == "" || len(skill) > maxSkillNameLen {
			conn.Close(websocket.StatusPolicyViolation, "invalid skill name")
			return
		}
	}

	// Register the worker
	workerID := uuid.New().String()
	worker := &hub.Worker{
		ID:          workerID,
		AgentName:   reg.AgentName,
		Description: reg.Description,
		Skills:      reg.Skills,
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
	}
	s.swarm.AddWorker(worker)

	wc := &workerConn{
		workerID: workerID,
		conn:     conn,
		cancel:   cancel,
	}
	s.mu.Lock()
	s.conns[workerID] = wc
	s.tasks[workerID] = make(map[string]struct{})
	s.mu.Unlock()

	s.logger.Info("worker connected",
		"id", workerID,
		"agent", reg.AgentName,
		"skills", reg.Skills,
	)

	// Send registered confirmation
	err = wc.write(ctx, Envelope{
		Type:      TypeRegistered,
		ID:        uuid.New().String(),
		InReplyTo: env.ID,
		From:      "leader",
		Timestamp: time.Now(),
		Payload: RegisteredPayload{
			AgentID:   workerID,
			SwarmName: s.swarm.Code(),
		},
	})
	if err != nil {
		s.logger.Error("failed to send registered", "error", err)
		s.cleanup(workerID)
		return
	}

	// Read messages until disconnect
	s.readLoop(ctx, wc)
	s.cleanup(workerID)
}

func (s *Server) readLoop(ctx context.Context, wc *workerConn) {
	for {
		var env Envelope
		if err := wsjson.Read(ctx, wc.conn, &env); err != nil {
			if ctx.Err() != nil {
				return // context cancelled
			}
			s.logger.Info("worker disconnected", "id", wc.workerID, "error", err)
			return
		}

		switch env.Type {
		case TypeHeartbeat:
			// Update last seen
			if _, ok := s.swarm.GetWorker(wc.workerID); ok {
				// TODO: add UpdateLastSeen method to swarm
			}

		case TypeTaskResult:
			s.handleTaskResult(wc.workerID, env)

		case TypeTaskError:
			s.handleTaskError(wc.workerID, env)

		case TypeTaskProgress:
			s.handleTaskProgress(env)

		default:
			s.logger.Warn("unknown message type from worker", "type", env.Type, "worker", wc.workerID)
		}
	}
}

func (s *Server) handleTaskResult(workerID string, env Envelope) {
	payloadBytes, err := json.Marshal(env.Payload)
	if err != nil {
		s.logger.Error("failed to marshal task result payload", "error", err)
		return
	}
	var result TaskResultPayload
	if err := json.Unmarshal(payloadBytes, &result); err != nil {
		s.logger.Error("failed to parse task result", "error", err)
		return
	}

	// Truncate oversized results before they reach the LLM
	if len(result.Result) > maxTaskResultLen {
		result.Result = result.Result[:maxTaskResultLen] + "\n[truncated]"
	}

	// Verify task ownership — only accept results from the assigned worker
	s.mu.Lock()
	taskSet, workerExists := s.tasks[workerID]
	if !workerExists {
		s.mu.Unlock()
		return
	}
	if _, owned := taskSet[result.TaskID]; !owned {
		s.mu.Unlock()
		s.logger.Warn("worker claimed result for unowned task",
			"worker", workerID, "task", result.TaskID)
		return
	}

	ch, hasPending := s.pending[result.TaskID]
	delete(s.pending, result.TaskID)
	delete(taskSet, result.TaskID)
	s.mu.Unlock()

	if hasPending {
		ch <- taskResponse{result: result.Result}
	}

	s.swarm.CompleteTask(result.TaskID, result.Result)
}

func (s *Server) handleTaskError(workerID string, env Envelope) {
	payloadBytes, err := json.Marshal(env.Payload)
	if err != nil {
		s.logger.Error("failed to marshal task error payload", "error", err)
		return
	}
	var taskErr TaskErrorPayload
	if err := json.Unmarshal(payloadBytes, &taskErr); err != nil {
		s.logger.Error("failed to parse task error", "error", err)
		return
	}

	// Verify task ownership
	s.mu.Lock()
	taskSet, workerExists := s.tasks[workerID]
	if !workerExists {
		s.mu.Unlock()
		return
	}
	if _, owned := taskSet[taskErr.TaskID]; !owned {
		s.mu.Unlock()
		s.logger.Warn("worker claimed error for unowned task",
			"worker", workerID, "task", taskErr.TaskID)
		return
	}

	ch, hasPending := s.pending[taskErr.TaskID]
	delete(s.pending, taskErr.TaskID)
	delete(taskSet, taskErr.TaskID)
	s.mu.Unlock()

	if hasPending {
		ch <- taskResponse{err: fmt.Errorf("worker error: %s", taskErr.Error)}
	}

	s.swarm.FailTask(taskErr.TaskID, taskErr.Error)
}

func (s *Server) handleTaskProgress(env Envelope) {
	payloadBytes, err := json.Marshal(env.Payload)
	if err != nil {
		s.logger.Error("failed to marshal task progress payload", "error", err)
		return
	}
	var progress TaskProgressPayload
	if err := json.Unmarshal(payloadBytes, &progress); err != nil {
		return
	}
	s.logger.Info("task progress", "task_id", progress.TaskID, "message", progress.Message)
}

// cleanup removes a worker and fails all its pending tasks.
// Collects state under lock, then calls Swarm methods outside the lock
// to avoid ABBA deadlock (Server.mu → Swarm.mu vs Swarm.mu → Server.mu).
func (s *Server) cleanup(workerID string) {
	// Step 1: collect pending tasks and channels under Server.mu
	type pendingTask struct {
		taskID string
		ch     chan taskResponse
	}
	var toFail []pendingTask

	s.mu.Lock()
	if taskSet, ok := s.tasks[workerID]; ok {
		for taskID := range taskSet {
			pt := pendingTask{taskID: taskID}
			if ch, pending := s.pending[taskID]; pending {
				pt.ch = ch
				delete(s.pending, taskID)
			}
			toFail = append(toFail, pt)
		}
		delete(s.tasks, workerID)
	}
	delete(s.conns, workerID)
	s.mu.Unlock()

	// Step 2: send errors and fail tasks outside the lock
	for _, pt := range toFail {
		if pt.ch != nil {
			pt.ch <- taskResponse{err: fmt.Errorf("worker disconnected")}
		}
		s.swarm.FailTask(pt.taskID, "worker disconnected")
	}

	s.swarm.RemoveWorker(workerID)
	s.logger.Info("worker cleaned up", "id", workerID)
}

// DispatchTask sends a task to a specific worker and blocks until the result
// is available or the context is cancelled.
func (s *Server) DispatchTask(ctx context.Context, worker hub.Worker, skill, prompt, taskContext string) (string, error) {
	s.mu.Lock()
	wc, ok := s.conns[worker.ID]
	s.mu.Unlock()

	if !ok {
		return "", fmt.Errorf("worker %q (%s) is not connected", worker.AgentName, worker.ID)
	}

	taskID := uuid.New().String()
	task := &hub.Task{
		ID:        taskID,
		Skill:     skill,
		Prompt:    prompt,
		WorkerID:  worker.ID,
		Status:    hub.TaskAssigned,
		CreatedAt: time.Now(),
	}
	s.swarm.AddTask(task)

	// Create result channel and track task against worker
	ch := make(chan taskResponse, 1)
	s.mu.Lock()
	s.pending[taskID] = ch
	if taskSet, exists := s.tasks[worker.ID]; exists {
		taskSet[taskID] = struct{}{}
	}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pending, taskID)
		if taskSet, exists := s.tasks[worker.ID]; exists {
			delete(taskSet, taskID)
		}
		s.mu.Unlock()
	}()

	// Send task request to worker — uses per-connection write mutex
	err := wc.write(ctx, Envelope{
		Type:      TypeTaskRequest,
		ID:        uuid.New().String(),
		From:      "leader",
		Timestamp: time.Now(),
		Payload: TaskRequestPayload{
			TaskID:  taskID,
			Skill:   skill,
			Prompt:  prompt,
			Context: taskContext,
		},
	})
	if err != nil {
		s.swarm.FailTask(taskID, err.Error())
		return "", fmt.Errorf("sending task to worker: %w", err)
	}

	// Wait for result
	select {
	case <-ctx.Done():
		s.swarm.FailTask(taskID, "context cancelled")
		return "", ctx.Err()
	case resp := <-ch:
		if resp.err != nil {
			return "", resp.err
		}
		return resp.result, nil
	}
}

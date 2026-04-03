package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
)

// TaskHandler is called when the worker receives a task from the leader.
// It should execute the task and return the result text.
type TaskHandler func(ctx context.Context, skill, prompt, taskContext string) (string, error)

// Client connects a worker agent to a leader via WebSocket.
type Client struct {
	leaderURL   string
	agentName   string
	description string
	skills      []string
	swarmCode   string
	handler     TaskHandler
	logger      *slog.Logger
	conn        *websocket.Conn
	writeMu     sync.Mutex     // serializes all writes to conn
	taskWg      sync.WaitGroup // tracks in-flight task goroutines
	agentID     string
}

// ClientOptions configures a worker client.
type ClientOptions struct {
	LeaderURL   string // e.g. "ws://leader:8080/ws/worker"
	AgentName   string
	Description string
	Skills      []string
	SwarmCode   string
	Handler     TaskHandler
	Logger      *slog.Logger
}

// NewClient creates a new worker client.
func NewClient(opts ClientOptions) *Client {
	return &Client{
		leaderURL:   opts.LeaderURL,
		agentName:   opts.AgentName,
		description: opts.Description,
		skills:      opts.Skills,
		swarmCode:   opts.SwarmCode,
		handler:     opts.Handler,
		logger:      opts.Logger,
	}
}

// write serializes all writes to the WebSocket connection.
func (c *Client) write(ctx context.Context, env Envelope) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return wsjson.Write(ctx, c.conn, env)
}

// Connect establishes a WebSocket connection to the leader, registers,
// and enters the message loop. Blocks until the context is cancelled
// or the connection drops.
func (c *Client) Connect(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, c.leaderURL, nil) //nolint:bodyclose // websocket.Dial doesn't return an http.Response that needs closing
	if err != nil {
		return fmt.Errorf("dialing leader at %s: %w", c.leaderURL, err)
	}
	c.conn = conn
	defer func() { _ = conn.CloseNow() }()

	// Send registration
	regID := uuid.New().String()
	err = c.write(ctx, Envelope{
		Type:      TypeRegister,
		ID:        regID,
		From:      c.agentName,
		Timestamp: time.Now(),
		Payload: RegisterPayload{
			AgentName:   c.agentName,
			Description: c.description,
			Skills:      c.skills,
			SwarmCode:   c.swarmCode,
		},
	})
	if err != nil {
		return fmt.Errorf("sending registration: %w", err)
	}

	// Wait for registered confirmation
	var env Envelope
	if err := wsjson.Read(ctx, conn, &env); err != nil {
		return fmt.Errorf("reading registration response: %w", err)
	}
	if env.Type != TypeRegistered {
		return fmt.Errorf("expected registered message, got %q", env.Type)
	}

	payloadBytes, err := json.Marshal(env.Payload)
	if err != nil {
		return fmt.Errorf("marshaling registered payload: %w", err)
	}
	var reg RegisteredPayload
	if err := json.Unmarshal(payloadBytes, &reg); err != nil {
		return fmt.Errorf("parsing registered payload: %w", err)
	}
	c.agentID = reg.AgentID

	c.logger.Info("registered with leader",
		"agent_id", c.agentID,
		"swarm", reg.SwarmName,
	)

	// Enter message loop — wait for in-flight tasks to drain on exit
	err = c.messageLoop(ctx)
	c.taskWg.Wait()
	return err
}

func (c *Client) messageLoop(ctx context.Context) error {
	for {
		var env Envelope
		if err := wsjson.Read(ctx, c.conn, &env); err != nil {
			if ctx.Err() != nil {
				return nil //nolint:nilerr // clean shutdown on context cancellation
			}
			return fmt.Errorf("reading message: %w", err)
		}

		switch env.Type {
		case TypeTaskRequest:
			c.taskWg.Go(func() {
				c.handleTask(ctx, env)
			})

		case TypeHeartbeat:
			_ = c.write(ctx, Envelope{
				Type:      TypeHeartbeat,
				ID:        uuid.New().String(),
				InReplyTo: env.ID,
				From:      c.agentName,
				Timestamp: time.Now(),
			})

		default:
			c.logger.Warn("unknown message type from leader", "type", env.Type)
		}
	}
}

func (c *Client) handleTask(ctx context.Context, env Envelope) {
	payloadBytes, err := json.Marshal(env.Payload)
	if err != nil {
		c.logger.Error("failed to marshal task request payload", "error", err)
		return
	}
	var req TaskRequestPayload
	if err := json.Unmarshal(payloadBytes, &req); err != nil {
		c.logger.Error("failed to parse task request", "error", err)
		return
	}

	c.logger.Info("received task", "task_id", req.TaskID, "skill", req.Skill)

	result, err := c.handler(ctx, req.Skill, req.Prompt, req.Context)
	if err != nil {
		c.logger.Error("task failed", "task_id", req.TaskID, "error", err)
		_ = c.write(ctx, Envelope{
			Type:      TypeTaskError,
			ID:        uuid.New().String(),
			InReplyTo: env.ID,
			From:      c.agentName,
			Timestamp: time.Now(),
			Payload: TaskErrorPayload{
				TaskID: req.TaskID,
				Error:  err.Error(),
			},
		})
		return
	}

	c.logger.Info("task completed", "task_id", req.TaskID)
	_ = c.write(ctx, Envelope{
		Type:      TypeTaskResult,
		ID:        uuid.New().String(),
		InReplyTo: env.ID,
		From:      c.agentName,
		Timestamp: time.Now(),
		Payload: TaskResultPayload{
			TaskID: req.TaskID,
			Result: result,
		},
	})
}

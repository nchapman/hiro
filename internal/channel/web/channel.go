// Package web implements the web UI channel — a WebSocket-based messaging
// provider that streams inference events to the browser in real time.
package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"

	"charm.land/fantasy"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/nchapman/hiro/internal/channel"
	"github.com/nchapman/hiro/internal/ipc"
)

// wsReadLimit is the WebSocket read limit (10 MB), large enough for file attachments.
const wsReadLimit = 10 * 1024 * 1024

const (
	maxAttachmentSize = 5 * 1024 * 1024 // 5 MB per attachment
	maxAttachments    = 10
	bytesPerMB        = 1024 * 1024
)

// Chat message type constants.
const (
	msgTypeConfig  = "config"
	msgTypeMessage = "message"
)

// ChatAttachment is a file attached to a chat message, base64-encoded.
type ChatAttachment struct {
	Filename  string `json:"filename"`
	Data      string `json:"data"`       // base64-encoded content
	MediaType string `json:"media_type"` // MIME type
}

// ChatMessage is a message sent or received over the chat WebSocket.
type ChatMessage struct {
	Type            string             `json:"type"`
	Role            string             `json:"role,omitempty"`
	Content         string             `json:"content,omitempty"`
	ToolCallID      string             `json:"tool_call_id,omitempty"`
	ToolName        string             `json:"tool_name,omitempty"`
	Input           string             `json:"input,omitempty"`
	Output          string             `json:"output,omitempty"`
	IsError         bool               `json:"is_error,omitempty"`
	IsMeta          bool               `json:"is_meta,omitempty"`
	Status          string             `json:"status,omitempty"`
	Usage           *channel.UsageInfo `json:"usage,omitempty"`
	Model           string             `json:"model,omitempty"`
	ReasoningEffort *string            `json:"reasoning_effort,omitempty"`
	AllowedTools    []string           `json:"allowed_tools,omitempty"`
	DisallowedTools []string           `json:"disallowed_tools,omitempty"`
	Attachments     []ChatAttachment   `json:"attachments,omitempty"`
}

// Channel is the web UI WebSocket channel. It manages multiple concurrent
// WebSocket connections, each bound to an agent instance via the Router.
type Channel struct {
	router  *channel.Router
	manager channel.ManagerInterface
	logger  *slog.Logger

	// connections tracks active WebSocket connections by conversation key.
	mu    sync.RWMutex
	conns map[string]*wsConn
}

// wsConn represents a single active WebSocket connection.
type wsConn struct {
	conn       *websocket.Conn
	ctx        context.Context
	instanceID string
	sessionID  string // per-channel session for this connection
}

// New creates a new web Channel.
func New(router *channel.Router, manager channel.ManagerInterface, logger *slog.Logger) *Channel {
	return &Channel{
		router:  router,
		manager: manager,
		logger:  logger.With("channel", "web"),
		conns:   make(map[string]*wsConn),
	}
}

// Name returns "web".
func (c *Channel) Name() string { return "web" }

// Trusted returns true — the web UI is allowed to execute all slash commands.
func (c *Channel) Trusted() bool { return true }

// Start is a no-op. WebSocket routes are registered on the HTTP mux by the
// API server; the channel handles connections as they arrive via HandleConn.
func (c *Channel) Start(_ context.Context) error { return nil }

// Stop closes all active connections.
func (c *Channel) Stop() error {
	c.mu.Lock()
	conns := make(map[string]*wsConn, len(c.conns))
	maps.Copy(conns, c.conns)
	c.conns = make(map[string]*wsConn)
	c.mu.Unlock()

	for key, wc := range conns {
		wc.conn.Close(websocket.StatusGoingAway, "server shutdown")
		c.router.Unbind(key)
	}
	return nil
}

// Deliver pushes notification events to a WebSocket connection. Called by
// the Router's notification fan-out. Returns ErrChannelClosed if the
// connection is gone.
func (c *Channel) Deliver(ctx context.Context, conversationKey string, events []ipc.ChatEvent, result channel.TurnResult) error {
	c.mu.RLock()
	wc, ok := c.conns[conversationKey]
	c.mu.RUnlock()
	if !ok {
		return channel.ErrChannelClosed
	}

	// Stream each event individually (same as user-triggered turns).
	for _, evt := range events {
		wireMsg := chatEventToMessage(evt)
		b, err := json.Marshal(wireMsg)
		if err != nil {
			continue
		}
		if err := wc.conn.Write(ctx, websocket.MessageText, b); err != nil {
			return channel.ErrChannelClosed
		}
	}

	// Send done marker.
	done := ChatMessage{Type: "done", Role: "assistant", Usage: result.Usage}
	if err := wsjson.Write(ctx, wc.conn, done); err != nil {
		return channel.ErrChannelClosed
	}
	return nil
}

// HandleConn handles a new WebSocket connection for chat. This is called
// by the API server's /ws/chat handler after auth and instance resolution.
// It blocks until the connection closes.
func (c *Channel) HandleConn(ctx context.Context, conn *websocket.Conn, instanceID, conversationKey string) {
	conn.SetReadLimit(wsReadLimit)

	// Ensure a per-channel session exists for this web connection.
	sessionID, err := c.manager.EnsureSession(ctx, instanceID, "web")
	if err != nil {
		c.logger.Warn("failed to ensure web session",
			"instance_id", instanceID,
			"error", err,
		)
		_ = wsjson.Write(ctx, conn, ChatMessage{Type: "error", Content: "Failed to create session: " + err.Error()})
		conn.Close(websocket.StatusInternalError, "session creation failed")
		return
	}

	wc := &wsConn{
		conn:       conn,
		ctx:        ctx,
		instanceID: instanceID,
		sessionID:  sessionID,
	}

	c.mu.Lock()
	c.conns[conversationKey] = wc
	c.mu.Unlock()

	b := c.router.Bind(conversationKey, "web", instanceID)
	b.SessionID = sessionID

	// Ensure a notification pump is running for this instance. Uses the
	// Router's app context (not the WebSocket context) so the pump survives
	// this connection closing. Idempotent — no-op if already running.
	c.router.EnsureNotificationPump(instanceID)

	// Send the session ID to the browser so it can fetch session-specific
	// history and usage.
	if sessionID != "" {
		_ = wsjson.Write(ctx, conn, ChatMessage{
			Type:    "session",
			Content: sessionID,
		})
	}

	defer func() {
		c.mu.Lock()
		delete(c.conns, conversationKey)
		c.mu.Unlock()
		c.router.Unbind(conversationKey)
	}()

	c.logger.Info("chat connected",
		"instance_id", instanceID,
		"session_id", sessionID,
		"conversation_key", conversationKey,
	)

	c.eventLoop(ctx, conn, instanceID, conversationKey)
}

// eventLoop reads user messages and dispatches them until the connection closes.
// Notifications are handled by the Router's pump; the web channel no longer
// drains them directly.
func (c *Channel) eventLoop(ctx context.Context, conn *websocket.Conn, instanceID, conversationKey string) {
	type readResult struct {
		msg ChatMessage
		err error
	}
	userMsgs := make(chan readResult, 1)
	go func() {
		for {
			var msg ChatMessage
			err := wsjson.Read(ctx, conn, &msg)
			select {
			case userMsgs <- readResult{msg, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case res := <-userMsgs:
			if res.err != nil {
				if ctx.Err() != nil {
					return
				}
				c.logger.Debug("chat connection closed", "error", res.err)
				return
			}
			if err := c.handleUserMessage(ctx, conn, instanceID, conversationKey, res.msg); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// handleUserMessage processes a single user message from the WebSocket.
func (c *Channel) handleUserMessage(ctx context.Context, conn *websocket.Conn, instanceID, conversationKey string, msg ChatMessage) error {
	// Config changes (model switch, reasoning toggle) are web-specific.
	if msg.Type == msgTypeConfig {
		return c.handleConfigMessage(ctx, conn, instanceID, msg)
	}

	if msg.Type != msgTypeMessage || (msg.Content == "" && len(msg.Attachments) == 0) {
		return nil
	}

	// Decode attachments into fantasy.FileParts.
	files, attErr := processAttachments(msg.Attachments)
	if attErr != nil {
		_ = wsjson.Write(ctx, conn, ChatMessage{Type: "error", Content: attErr.Error()})
		return nil //nolint:nilerr // error reported to client; don't break connection
	}

	// Look up the session ID from the wsConn.
	c.mu.RLock()
	wc := c.conns[conversationKey]
	var sessionID string
	if wc != nil {
		sessionID = wc.sessionID
	}
	c.mu.RUnlock()

	// Build the inbound message and dispatch through the Router.
	inbound := channel.InboundMessage{
		ConversationKey: conversationKey,
		InstanceID:      instanceID,
		SessionID:       sessionID,
		ChannelName:     "web",
		ChannelKey:      "web",
		Text:            msg.Content,
		Files:           files,
		OnEvent:         c.makeOnEvent(ctx, conn, conversationKey),
		OnDone:          c.makeOnDone(ctx, conn),
	}

	return c.router.Dispatch(ctx, inbound)
}

// handleConfigMessage processes a config change (model switch, reasoning toggle).
// This is web-specific and does not go through the Router.
func (c *Channel) handleConfigMessage(ctx context.Context, conn *websocket.Conn, instanceID string, msg ChatMessage) error {
	if err := c.manager.UpdateInstanceConfig(ctx, instanceID, msg.Model, msg.ReasoningEffort, msg.AllowedTools, msg.DisallowedTools); err != nil {
		c.logger.Warn("config update failed", "instance_id", instanceID, "error", err)
		_ = wsjson.Write(ctx, conn, ChatMessage{Type: "error", Content: err.Error()})
	} else {
		c.logger.Info("config updated", "instance_id", instanceID, "model", msg.Model)
		_ = wsjson.Write(ctx, conn, ChatMessage{Type: "system", Content: "Configuration updated."})
	}
	done := ChatMessage{Type: "done"}
	return wsjson.Write(ctx, conn, done)
}

// makeOnEvent builds a streaming callback that writes inference events to
// the WebSocket as JSON. It also intercepts "session" events to update the
// wsConn's sessionID when a new session is created (e.g. after /clear).
func (c *Channel) makeOnEvent(ctx context.Context, conn *websocket.Conn, conversationKey string) func(ipc.ChatEvent) error {
	return func(evt ipc.ChatEvent) error {
		// Update the wsConn's session ID when the server sends a new session event.
		if evt.Type == "session" && evt.Content != "" {
			c.mu.Lock()
			if wc, ok := c.conns[conversationKey]; ok {
				wc.sessionID = evt.Content
			}
			c.mu.Unlock()
		}

		wireMsg := chatEventToMessage(evt)
		b, err := json.Marshal(wireMsg)
		if err != nil {
			return err
		}
		return conn.Write(ctx, websocket.MessageText, b)
	}
}

// makeOnDone builds an end-of-turn callback that writes the done marker
// with usage data to the WebSocket.
func (c *Channel) makeOnDone(ctx context.Context, conn *websocket.Conn) func(channel.TurnResult) error {
	return func(result channel.TurnResult) error {
		done := ChatMessage{Type: "done", Role: "assistant", Usage: result.Usage}
		return wsjson.Write(ctx, conn, done)
	}
}

// chatEventToMessage converts an ipc.ChatEvent to a ChatMessage for the wire.
func chatEventToMessage(evt ipc.ChatEvent) ChatMessage {
	return ChatMessage{
		Type:       evt.Type,
		Role:       "assistant",
		Content:    evt.Content,
		ToolCallID: evt.ToolCallID,
		ToolName:   evt.ToolName,
		Input:      evt.Input,
		Output:     evt.Output,
		IsError:    evt.IsError,
		IsMeta:     evt.IsMeta,
		Status:     evt.Status,
	}
}

// supportedMIME returns true for MIME types we accept as attachments.
func supportedMIME(mediaType string) bool {
	switch {
	case strings.HasPrefix(mediaType, "image/"):
		return true
	case strings.HasPrefix(mediaType, "text/"):
		return true
	case mediaType == "application/json",
		mediaType == "application/xml",
		mediaType == "application/yaml",
		mediaType == "application/x-yaml",
		mediaType == "application/pdf":
		return true
	}
	return false
}

// processAttachments decodes base64 attachments into fantasy.FileParts.
func processAttachments(attachments []ChatAttachment) ([]fantasy.FilePart, error) {
	if len(attachments) == 0 {
		return nil, nil
	}
	if len(attachments) > maxAttachments {
		return nil, fmt.Errorf("too many attachments (max %d)", maxAttachments)
	}

	var files []fantasy.FilePart
	for _, att := range attachments {
		if !supportedMIME(att.MediaType) {
			return nil, fmt.Errorf("unsupported file type %q for %s", att.MediaType, att.Filename)
		}
		if len(att.Data) > maxAttachmentSize*4/3+1024 {
			return nil, fmt.Errorf("attachment %s exceeds %d MB limit", att.Filename, maxAttachmentSize/bytesPerMB)
		}
		data, err := base64.StdEncoding.DecodeString(att.Data)
		if err != nil {
			return nil, fmt.Errorf("invalid base64 for %s: %w", att.Filename, err)
		}
		if len(data) > maxAttachmentSize {
			return nil, fmt.Errorf("attachment %s exceeds %d MB limit", att.Filename, maxAttachmentSize/bytesPerMB)
		}
		files = append(files, fantasy.FilePart{
			Filename:  att.Filename,
			Data:      data,
			MediaType: att.MediaType,
		})
	}
	return files, nil
}

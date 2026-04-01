package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"charm.land/fantasy"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/nchapman/hiro/internal/agent"
	"github.com/nchapman/hiro/internal/config"
	"github.com/nchapman/hiro/internal/ipc"
	"github.com/nchapman/hiro/internal/watcher"
)

// ChatAttachment is a file attached to a chat message, base64-encoded.
type ChatAttachment struct {
	Filename  string `json:"filename"`
	Data      string `json:"data"`       // base64-encoded content
	MediaType string `json:"media_type"` // MIME type (image/png, text/plain, etc.)
}

// ChatMessage is a message sent or received over the chat WebSocket.
// For text deltas: type="delta", content="..."
// For tool calls: type="tool_call", tool_call_id, tool_name, input
// For tool results: type="tool_result", tool_call_id, content (output), is_error
// For control: type="done"|"error"|"system"|"message"
// For config changes: type="config", model, reasoning_effort
type ChatMessage struct {
	Type            string           `json:"type"`
	Role            string           `json:"role,omitempty"`
	Content         string           `json:"content,omitempty"`
	ToolCallID      string           `json:"tool_call_id,omitempty"`
	ToolName        string           `json:"tool_name,omitempty"`
	Input           string           `json:"input,omitempty"`
	Output          string           `json:"output,omitempty"`
	IsError         bool             `json:"is_error,omitempty"`
	IsMeta          bool             `json:"is_meta,omitempty"`
	Status          string           `json:"status,omitempty"`
	Usage           *UsageInfo       `json:"usage,omitempty"`
	Model           string           `json:"model,omitempty"`
	ReasoningEffort *string          `json:"reasoning_effort,omitempty"`
	Attachments     []ChatAttachment `json:"attachments,omitempty"`
}

// SetManager sets the agent manager and leader agent ID for handling chat.
func (s *Server) SetManager(m *agent.Manager, leaderID string) {
	s.manager = m
	s.leaderID = leaderID
}

// SetStartManager sets the callback to start the agent manager.
// Used by the setup endpoint to boot the manager after initial config.
func (s *Server) SetStartManager(fn func() error) {
	s.startManager = fn
}

// SetWatcher sets the filesystem watcher for pushing live updates.
func (s *Server) SetWatcher(w *watcher.Watcher) {
	s.watcher = w
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	// Enforce auth on WebSocket upgrade (browser sends cookies automatically).
	if s.cp != nil && !s.cp.NeedsSetup() && !s.isAuthenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if !s.hasManager() || s.leaderID == "" {
		http.Error(w, "no agent configured", http.StatusServiceUnavailable)
		return
	}

	// Allow targeting a specific instance via query param, default to leader
	instanceID := r.URL.Query().Get("instance_id")
	if instanceID == "" {
		instanceID = s.leaderID
	}
	info, ok := s.manager.GetInstance(instanceID)
	if !ok || info.Mode == config.ModeEphemeral {
		http.Error(w, "instance not found", http.StatusNotFound)
		return
	}
	if info.Status == agent.InstanceStatusStopped {
		http.Error(w, "instance is stopped", http.StatusConflict)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Restrict to same-origin. In development, Vite proxies WebSocket
		// requests so the origin matches. In production, the embedded UI
		// is served from the same host.
		OriginPatterns: []string{r.Host},
	})
	if err != nil {
		s.logger.Error("chat websocket accept failed", "error", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	s.logger.Info("chat connected", "instance_id", instanceID)

	// Allow large messages for file attachments (default 32KB is too small).
	conn.SetReadLimit(10 * 1024 * 1024) // 10 MB

	ctx := r.Context()

	// onEvent streams inference events to the WebSocket. Shared between
	// user-triggered and notification-triggered turns.
	onEvent := func(evt ipc.ChatEvent) error {
		wireMsg := ChatMessage{
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
		b, err := json.Marshal(wireMsg)
		if err != nil {
			return err
		}
		return conn.Write(ctx, websocket.MessageText, b)
	}

	// sendDone writes the end-of-turn marker with usage data.
	sendDone := func() error {
		done := ChatMessage{Type: "done", Role: "assistant"}
		done.Usage = s.buildUsageInfo(instanceID)
		return wsjson.Write(ctx, conn, done)
	}

	// Read user messages in a goroutine so we can select on both user
	// input and notification signals.
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

	// Get the notification queue for this instance (may be nil if no loop).
	var notifyReady <-chan struct{}
	if q := s.manager.InstanceNotifications(instanceID); q != nil {
		notifyReady = q.Ready()
	}

	for {
		select {
		case res := <-userMsgs:
			if res.err != nil {
				if ctx.Err() != nil {
					return
				}
				s.logger.Debug("chat connection closed", "error", res.err)
				return
			}
			if err := s.handleUserMessage(ctx, conn, instanceID, res.msg, onEvent, sendDone); err != nil {
				return
			}

		case <-notifyReady:
			if err := s.drainNotifications(ctx, instanceID, onEvent, sendDone); err != nil {
				return
			}

		case <-ctx.Done():
			return
		}
	}
}

// handleUserMessage processes a single user message from the WebSocket.
// Returns a non-nil error if the connection should be closed.
func (s *Server) handleUserMessage(ctx context.Context, conn *websocket.Conn, instanceID string, msg ChatMessage, onEvent func(ipc.ChatEvent) error, sendDone func() error) error {
	// Handle config changes (model switch, reasoning toggle).
	if msg.Type == "config" {
		if err := s.manager.UpdateInstanceConfig(ctx, instanceID, msg.Model, msg.ReasoningEffort); err != nil {
			s.logger.Warn("config update failed", "instance_id", instanceID, "error", err)
			_ = wsjson.Write(ctx, conn, ChatMessage{Type: "error", Content: err.Error()})
		} else {
			s.logger.Info("config updated", "instance_id", instanceID, "model", msg.Model)
			_ = wsjson.Write(ctx, conn, ChatMessage{Type: "system", Content: "Configuration updated."})
		}
		done := ChatMessage{Type: "done", Usage: s.buildUsageInfo(instanceID)}
		return wsjson.Write(ctx, conn, done)
	}

	if msg.Type != "message" || (msg.Content == "" && len(msg.Attachments) == 0) {
		return nil
	}

	// Intercept slash commands before processing attachments.
	if s.cmdHandler != nil && strings.HasPrefix(msg.Content, "/") {
		result, err := s.cmdHandler.HandleCommand(msg.Content)
		if err == nil {
			// Recognized command — send result directly, don't forward to agent.
			if writeErr := wsjson.Write(ctx, conn, ChatMessage{Type: "system", Content: result}); writeErr != nil {
				return writeErr
			}
			return wsjson.Write(ctx, conn, ChatMessage{Type: "done"})
		}
		// Unrecognized command — fall through to agent as normal message.
	}

	// Decode attachments into fantasy.FileParts.
	files, attErr := processAttachments(msg.Attachments)
	if attErr != nil {
		_ = wsjson.Write(ctx, conn, ChatMessage{Type: "error", Content: attErr.Error()})
		return nil
	}

	// Stream response — agent process owns the conversation.
	_, streamErr := s.manager.SendMessageWithFiles(ctx, instanceID, msg.Content, files, onEvent)
	if streamErr != nil {
		s.logger.Warn("chat message failed", "instance_id", instanceID, "error", streamErr)
		_ = wsjson.Write(ctx, conn, ChatMessage{Type: "error", Content: streamErr.Error()})
	}
	return sendDone()
}

// drainNotifications processes all pending notifications for an instance,
// triggering a meta inference turn for each. Session-scoped notifications are
// discarded if they don't match the active session. Returns a non-nil error
// if the connection should be closed.
func (s *Server) drainNotifications(ctx context.Context, instanceID string, onEvent func(ipc.ChatEvent) error, sendDone func() error) error {
	q := s.manager.InstanceNotifications(instanceID)
	if q == nil {
		return nil
	}

	activeSession := s.manager.ActiveSessionID(instanceID)
	// Drain takes a snapshot. Notifications pushed during a meta turn (e.g. by
	// a tool call) are deferred to the next select cycle — intentional to
	// prevent unbounded recursive notification processing.
	notifications := q.Drain()

	for _, n := range notifications {
		// Discard session-scoped notifications that don't match the active session.
		if n.SessionID != "" && n.SessionID != activeSession {
			s.logger.Info("discarding stale notification",
				"instance_id", instanceID,
				"source", n.Source,
				"notification_session", n.SessionID,
				"active_session", activeSession,
			)
			continue
		}

		s.logger.Info("processing notification",
			"instance_id", instanceID,
			"source", n.Source,
			"content_length", len(n.Content),
		)
		_, turnErr := s.manager.SendMetaMessage(ctx, instanceID, n.Content, onEvent)
		if turnErr != nil {
			s.logger.Warn("notification turn failed", "instance_id", instanceID, "error", turnErr)
		}

		// Always send done so the frontend exits streaming state.
		if err := sendDone(); err != nil {
			return err
		}

		// Terminal errors (instance stopped/gone) — stop draining.
		if turnErr != nil && (errors.Is(turnErr, agent.ErrInstanceNotFound) ||
			strings.Contains(turnErr.Error(), "is stopped")) {
			return nil
		}
	}
	return nil
}

// buildUsageInfo queries the platform DB for instance usage and returns it
// as a UsageInfo struct for inclusion in WebSocket messages.
func (s *Server) buildUsageInfo(instanceID string) *UsageInfo {
	if s.pdb == nil {
		return nil
	}

	var model string
	if s.manager != nil {
		if info, ok := s.manager.GetInstance(instanceID); ok {
			model = info.Model
		}
	}

	// Use the active session for usage tracking.
	sessionID := instanceID
	if s.manager != nil {
		if sid := s.manager.ActiveSessionID(instanceID); sid != "" {
			sessionID = sid
		}
	}

	info := s.buildUsageInfoForSession(sessionID, model)
	return &info
}

const (
	maxAttachmentSize = 5 * 1024 * 1024 // 5 MB per attachment
	maxAttachments    = 10
)

// supportedMIME returns true for MIME types we accept as attachments.
// Fantasy handles the provider-specific encoding: images become image blocks,
// text/* becomes Anthropic document blocks, etc.
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
// All supported file types are passed through as FileParts — fantasy and the
// LLM provider handle type-specific encoding (images, text documents, PDFs).
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
		// Reject before decoding to avoid allocating oversized buffers.
		if len(att.Data) > maxAttachmentSize*4/3+1024 {
			return nil, fmt.Errorf("attachment %s exceeds %d MB limit", att.Filename, maxAttachmentSize/(1024*1024))
		}
		data, err := base64.StdEncoding.DecodeString(att.Data)
		if err != nil {
			return nil, fmt.Errorf("invalid base64 for %s: %w", att.Filename, err)
		}
		if len(data) > maxAttachmentSize {
			return nil, fmt.Errorf("attachment %s exceeds %d MB limit", att.Filename, maxAttachmentSize/(1024*1024))
		}
		files = append(files, fantasy.FilePart{
			Filename:  att.Filename,
			Data:      data,
			MediaType: att.MediaType,
		})
	}
	return files, nil
}

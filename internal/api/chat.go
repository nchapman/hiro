package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"charm.land/fantasy"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/nchapman/hivebot/internal/agent"
	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/ipc"
	"github.com/nchapman/hivebot/internal/watcher"
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

	// Allow large messages for file attachments (default 32KB is too small).
	conn.SetReadLimit(10 * 1024 * 1024) // 10 MB

	ctx := r.Context()

	for {
		// Read user message
		var msg ChatMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logger.Debug("chat connection closed", "error", err)
			return
		}

		// Handle config changes (model switch, reasoning toggle).
		if msg.Type == "config" {
			if err := s.manager.UpdateInstanceConfig(ctx, instanceID, msg.Model, msg.ReasoningEffort); err != nil {
				_ = wsjson.Write(ctx, conn, ChatMessage{Type: "error", Content: err.Error()})
			} else {
				_ = wsjson.Write(ctx, conn, ChatMessage{Type: "system", Content: "Configuration updated."})
			}
			done := ChatMessage{Type: "done", Usage: s.buildUsageInfo(instanceID)}
			if writeErr := wsjson.Write(ctx, conn, done); writeErr != nil {
				return
			}
			continue
		}

		if msg.Type != "message" || (msg.Content == "" && len(msg.Attachments) == 0) {
			continue
		}

		// Intercept slash commands before processing attachments.
		if s.cmdHandler != nil && strings.HasPrefix(msg.Content, "/") {
			result, err := s.cmdHandler.HandleCommand(msg.Content)
			if err == nil {
				// Recognized command — send result directly, don't forward to agent.
				resp := ChatMessage{Type: "system", Content: result}
				if writeErr := wsjson.Write(ctx, conn, resp); writeErr != nil {
					return
				}
				done := ChatMessage{Type: "done"}
				if writeErr := wsjson.Write(ctx, conn, done); writeErr != nil {
					return
				}
				continue
			}
			// Unrecognized command — fall through to agent as normal message.
		}

		// Decode attachments into fantasy.FileParts.
		files, attErr := processAttachments(msg.Attachments)
		if attErr != nil {
			_ = wsjson.Write(ctx, conn, ChatMessage{Type: "error", Content: attErr.Error()})
			continue
		}

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
				Status:     evt.Status,
			}
			b, err := json.Marshal(wireMsg)
			if err != nil {
				return err
			}
			return conn.Write(ctx, websocket.MessageText, b)
		}

		// Stream response — agent process owns the conversation.
		_, streamErr := s.manager.SendMessageWithFiles(ctx, instanceID, msg.Content, files, onEvent)

		if streamErr != nil {
			errMsg := ChatMessage{Type: "error", Content: streamErr.Error()}
			if writeErr := wsjson.Write(ctx, conn, errMsg); writeErr != nil {
				return
			}
			// Still send done so the frontend exits streaming state.
			done := ChatMessage{Type: "done", Usage: s.buildUsageInfo(instanceID)}
			if writeErr := wsjson.Write(ctx, conn, done); writeErr != nil {
				return
			}
			continue
		}

		// Signal end of response with usage data.
		done := ChatMessage{Type: "done", Role: "assistant"}
		done.Usage = s.buildUsageInfo(instanceID)
		if err := wsjson.Write(ctx, conn, done); err != nil {
			return
		}
	}
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

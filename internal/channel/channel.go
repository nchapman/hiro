// Package channel defines the abstraction for messaging providers.
// Each provider (web, telegram, slack) implements the Channel interface
// to send and receive messages to/from agent instances. The Router
// dispatches inbound messages, handles slash commands, and fans out
// notifications to all channels with active bindings.
package channel

import (
	"context"
	"errors"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/agent"
	"github.com/nchapman/hiro/internal/config"
	"github.com/nchapman/hiro/internal/inference"
	"github.com/nchapman/hiro/internal/ipc"
	"github.com/nchapman/hiro/internal/models"
	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

// ErrChannelClosed is returned by Deliver when the conversation endpoint is
// gone (e.g. WebSocket disconnected). The Router uses this to unbind the
// conversation key automatically.
var ErrChannelClosed = errors.New("channel closed")

// ErrNoBinding is returned by Router.Dispatch when no binding exists for the
// conversation key.
var ErrNoBinding = errors.New("no binding for conversation key")

// ErrUntrustedCommand is returned when an untrusted channel attempts a
// sensitive slash command (/secrets, /tools).
var ErrUntrustedCommand = errors.New("command restricted to trusted channels")

// Channel is a messaging provider that sends and receives messages to/from
// agent instances. Each channel owns its inbound transport (WebSocket,
// webhook, polling) and calls Router.Dispatch to send user messages to
// the inference loop.
type Channel interface {
	// Name returns the channel type identifier ("web", "telegram", "slack").
	Name() string

	// Trusted reports whether this channel is allowed to execute sensitive
	// slash commands (/secrets, /tools, /cluster).
	Trusted() bool

	// Start begins listening for inbound messages. The context controls
	// the channel's lifetime.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the channel.
	Stop() error

	// Deliver pushes collected events from a notification-triggered inference
	// turn to a specific conversation. For the web channel this streams
	// events over the WebSocket; for telegram/slack it sends a formatted
	// message. Returns ErrChannelClosed if the conversation is gone.
	// Channels are responsible for message formatting (length splitting,
	// markdown conversion, etc.).
	Deliver(ctx context.Context, conversationKey string, events []ipc.ChatEvent, result TurnResult) error
}

// InboundMessage is the normalized form of a user message from any channel.
// The channel constructs this and passes it to Router.Dispatch.
type InboundMessage struct {
	// ConversationKey is the channel-specific conversation identifier.
	// Examples: "web:<connID>", "tg:<chatID>", "slack:<channel>:<threadTS>"
	ConversationKey string

	// InstanceID is the target instance, resolved from the binding.
	InstanceID string

	// ChannelName identifies which channel sent this (for trust checks).
	ChannelName string

	// Text is the user's message content.
	Text string

	// Files are optional attachments (images, documents). Nil for text-only.
	Files []fantasy.FilePart

	// OnEvent is the streaming callback, called for each inference event.
	// The web channel writes JSON to the WebSocket; telegram/slack buffer
	// text deltas for later delivery.
	OnEvent func(ipc.ChatEvent) error

	// OnDone is called when the inference turn completes (or a slash command
	// produces its response). Channels use this to send the "done" marker
	// (web) or the buffered complete message (telegram/slack).
	OnDone func(TurnResult) error
}

// TurnResult carries completion data from an inference turn so that channels
// don't need to query the database directly.
type TurnResult struct {
	// Response is the complete text response from the agent.
	Response string

	// Usage contains token counts and cost data. Nil if unavailable.
	Usage *UsageInfo
}

// UsageInfo mirrors the per-turn and session usage data. Defined here to
// avoid a circular dependency between channel and api packages.
type UsageInfo struct {
	TurnInputTokens  int64   `json:"turn_input_tokens"`
	TurnOutputTokens int64   `json:"turn_output_tokens"`
	TurnCost         float64 `json:"turn_cost"`

	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`

	SessionInputTokens  int64   `json:"session_input_tokens"`
	SessionOutputTokens int64   `json:"session_output_tokens"`
	SessionTotalTokens  int64   `json:"session_total_tokens"`
	SessionCost         float64 `json:"session_cost"`
	EventCount          int64   `json:"event_count"`

	ContextWindow int    `json:"context_window"`
	Model         string `json:"model,omitempty"`
}

// CommandHandler handles slash commands from the chat interface.
type CommandHandler interface {
	HandleCommand(input string) (string, error)
}

// ManagerInterface is the narrow interface the Router needs from the
// agent.Manager. Defining it here keeps the Router testable without
// importing the full Manager.
type ManagerInterface interface {
	SendMessage(ctx context.Context, instanceID, message string, onEvent func(ipc.ChatEvent) error) (string, error)
	SendMessageWithFiles(ctx context.Context, instanceID, message string, files []fantasy.FilePart, onEvent func(ipc.ChatEvent) error) (string, error)
	SendMetaMessage(ctx context.Context, instanceID, message string, onEvent func(ipc.ChatEvent) error) (string, error)
	NewSession(instanceID string) (string, error)
	UpdateInstanceConfig(ctx context.Context, instanceID, model string, reasoningEffort *string) error
	InstanceNotifications(instanceID string) *inference.NotificationQueue
	ActiveSessionID(instanceID string) string
	GetInstance(instanceID string) (agent.InstanceInfo, bool)
	InstanceByAgentName(name string) (id string, running bool)
}

// UsageQuerier builds usage info from the platform database. The Router
// uses this to populate TurnResult.Usage so channels don't need DB access.
type UsageQuerier struct {
	PDB     *platformdb.DB
	Manager ManagerInterface
}

// BuildUsageInfo queries usage data for the active session of an instance.
func (q *UsageQuerier) BuildUsageInfo(ctx context.Context, instanceID string) *UsageInfo {
	if q == nil || q.PDB == nil {
		return nil
	}

	var model string
	if q.Manager != nil {
		if info, ok := q.Manager.GetInstance(instanceID); ok {
			model = info.Model
		}
	}

	sessionID := instanceID
	if q.Manager != nil {
		if sid := q.Manager.ActiveSessionID(instanceID); sid != "" {
			sessionID = sid
		}
	}

	info := &UsageInfo{
		ContextWindow: models.ContextWindow(model),
		Model:         model,
	}

	if usage, err := q.PDB.GetSessionUsage(ctx, sessionID); err == nil {
		info.SessionInputTokens = usage.TotalInputTokens
		info.SessionOutputTokens = usage.TotalOutputTokens
		info.SessionTotalTokens = usage.TotalInputTokens + usage.TotalOutputTokens
		info.SessionCost = usage.TotalCost
		info.EventCount = usage.EventCount
	}

	if turn, ok, err := q.PDB.GetLastTurnUsage(ctx, sessionID); err == nil && ok {
		info.TurnInputTokens = turn.TotalInputTokens
		info.TurnOutputTokens = turn.TotalOutputTokens
		info.TurnCost = turn.TotalCost
	}

	if last, ok, err := q.PDB.GetLastUsageEvent(ctx, sessionID); err == nil && ok {
		info.PromptTokens = last.InputTokens
		info.CompletionTokens = last.OutputTokens
	}

	return info
}

// Binding maps a channel conversation to an agent instance.
type Binding struct {
	ConversationKey string
	ChannelName     string
	Target          string // instance ID or agent name (resolved lazily)
	Channel         Channel
}

// ResolveInstanceID resolves the binding's target to an instance ID.
// If the target is already an instance ID (found in the manager), it is
// returned directly. Otherwise it is treated as an agent name and resolved
// via InstanceByAgentName. Returns empty string if resolution fails.
func (b *Binding) ResolveInstanceID(mgr ManagerInterface) string {
	// Try as instance ID first.
	if _, ok := mgr.GetInstance(b.Target); ok {
		return b.Target
	}
	// Try as agent name.
	if id, running := mgr.InstanceByAgentName(b.Target); id != "" && running {
		return id
	}
	return ""
}

// Compile-time check that agent.Manager satisfies ManagerInterface.
var _ ManagerInterface = (*agent.Manager)(nil)

// Compile-time check: config.AgentMode is used in InstanceInfo.
var _ = config.ModeEphemeral

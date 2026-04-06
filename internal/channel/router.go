package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nchapman/hiro/internal/agent"
	"github.com/nchapman/hiro/internal/inference"
	"github.com/nchapman/hiro/internal/ipc"
)

// deliverTimeout is the maximum time to wait for a single channel Deliver call
// during notification fan-out.
const deliverTimeout = 30 * time.Second

// sensitiveCommands are slash commands restricted to trusted channels.
var sensitiveCommands = map[string]bool{
	"secrets": true,
	"tools":   true,
	"cluster": true,
}

// Router dispatches inbound messages to agent instances and routes
// notifications back to the originating channels. It is the bridge
// between channels and the Manager.
type Router struct {
	manager    ManagerInterface
	cmdHandler CommandHandler
	usage      *UsageQuerier

	mu       sync.RWMutex
	channels map[string]Channel // name → channel

	bindMu   sync.RWMutex
	bindings map[string]*Binding // conversationKey → binding

	// pumpMu protects the pumps map and pump lifecycle.
	pumpMu sync.Mutex
	pumps  map[string]context.CancelFunc // instanceID → cancel

	logger *slog.Logger
}

// NewRouter creates a Router wired to the given Manager and command handler.
func NewRouter(mgr ManagerInterface, cmdHandler CommandHandler, usage *UsageQuerier, logger *slog.Logger) *Router {
	return &Router{
		manager:    mgr,
		cmdHandler: cmdHandler,
		usage:      usage,
		channels:   make(map[string]Channel),
		bindings:   make(map[string]*Binding),
		pumps:      make(map[string]context.CancelFunc),
		logger:     logger.With("component", "router"),
	}
}

// Register adds a channel to the router.
func (r *Router) Register(ch Channel) {
	r.mu.Lock()
	r.channels[ch.Name()] = ch
	r.mu.Unlock()
}

// Channel returns the named channel, or nil.
func (r *Router) Channel(name string) Channel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.channels[name]
}

// GetBinding returns the binding for a conversation key, or nil.
func (r *Router) GetBinding(conversationKey string) *Binding {
	r.bindMu.RLock()
	defer r.bindMu.RUnlock()
	return r.bindings[conversationKey]
}

// Manager returns the underlying manager interface. Used by channels that
// need to resolve instance IDs from bindings.
func (r *Router) Manager() ManagerInterface {
	return r.manager
}

// Bind creates a mapping from a conversation key to a target (instance ID or
// agent name). The channelName identifies which channel owns this binding.
// Returns the created binding.
func (r *Router) Bind(conversationKey, channelName, target string) *Binding {
	r.mu.RLock()
	ch := r.channels[channelName]
	r.mu.RUnlock()

	b := &Binding{
		ConversationKey: conversationKey,
		ChannelName:     channelName,
		Target:          target,
		Channel:         ch,
	}
	r.bindMu.Lock()
	r.bindings[conversationKey] = b
	r.bindMu.Unlock()
	return b
}

// Unbind removes a conversation binding.
func (r *Router) Unbind(conversationKey string) {
	r.bindMu.Lock()
	delete(r.bindings, conversationKey)
	r.bindMu.Unlock()
}

// bindingsForInstance returns all bindings targeting the given instance ID.
// Snapshots bindings under the lock, then resolves instance IDs outside it
// to avoid holding bindMu while calling into the manager (which has its own
// locks — keeping them separate prevents lock-order inversions).
func (r *Router) bindingsForInstance(instanceID string) []*Binding {
	r.bindMu.RLock()
	snapshot := make([]*Binding, 0, len(r.bindings))
	for _, b := range r.bindings {
		snapshot = append(snapshot, b)
	}
	r.bindMu.RUnlock()

	var result []*Binding
	for _, b := range snapshot {
		if b.ResolveInstanceID(r.manager) == instanceID {
			result = append(result, b)
		}
	}
	return result
}

// Dispatch handles an inbound message: intercepts slash commands,
// resolves the binding, and calls Manager.SendMessage.
func (r *Router) Dispatch(ctx context.Context, msg InboundMessage) error {
	// Slash command interception.
	if strings.HasPrefix(msg.Text, "/") {
		return r.handleSlashCommand(ctx, msg)
	}

	// Route to Manager.
	var response string
	var err error
	if len(msg.Files) > 0 {
		response, err = r.manager.SendMessageWithFiles(ctx, msg.InstanceID, msg.Text, msg.Files, msg.OnEvent)
	} else {
		response, err = r.manager.SendMessage(ctx, msg.InstanceID, msg.Text, msg.OnEvent)
	}

	if err != nil {
		r.logger.Warn("message dispatch failed",
			"instance_id", msg.InstanceID,
			"channel", msg.ChannelName,
			"error", err,
		)
		// Send error to the channel.
		_ = msg.OnEvent(ipc.ChatEvent{Type: "error", Content: err.Error()})
	}

	return msg.OnDone(TurnResult{
		Response: response,
		Usage:    r.usage.BuildUsageInfo(ctx, msg.InstanceID),
	})
}

// handleSlashCommand dispatches a slash command from any channel.
func (r *Router) handleSlashCommand(ctx context.Context, msg InboundMessage) error {
	trimmed := strings.TrimPrefix(msg.Text, "/")
	cmd := strings.Fields(trimmed)
	if len(cmd) == 0 {
		_ = msg.OnEvent(ipc.ChatEvent{Type: "system", Content: "Type /help for available commands."})
		return msg.OnDone(TurnResult{})
	}

	noun := cmd[0]

	// /clear — start a new session.
	if noun == "clear" {
		return r.handleClearCommand(ctx, msg)
	}

	// Trust check for sensitive commands.
	if sensitiveCommands[noun] {
		r.mu.RLock()
		ch := r.channels[msg.ChannelName]
		r.mu.RUnlock()
		if ch != nil && !ch.Trusted() {
			_ = msg.OnEvent(ipc.ChatEvent{
				Type:    "system",
				Content: fmt.Sprintf("/%s is only available from the web interface.", noun),
			})
			return msg.OnDone(TurnResult{})
		}
	}

	// Delegate to command handler.
	if r.cmdHandler != nil {
		return r.dispatchCommand(ctx, msg)
	}

	_ = msg.OnEvent(ipc.ChatEvent{
		Type:    "system",
		Content: fmt.Sprintf("Unknown command: %s\nType /help for available commands.", noun),
	})
	return msg.OnDone(TurnResult{})
}

// handleClearCommand processes the /clear slash command.
func (r *Router) handleClearCommand(_ context.Context, msg InboundMessage) error {
	// Emit clear event (web channel uses this to reset the UI).
	_ = msg.OnEvent(ipc.ChatEvent{Type: "clear"})
	_ = msg.OnDone(TurnResult{})

	// NewSession runs in the background with a detached context so it
	// completes even if the caller disconnects. Errors are logged only —
	// OnDone has already been called so sending events would violate the
	// turn protocol.
	go func() {
		if _, err := r.manager.NewSession(msg.InstanceID); err != nil {
			r.logger.Warn("failed to create new session after /clear",
				"instance_id", msg.InstanceID,
				"error", err,
			)
		}
	}()
	return nil
}

// dispatchCommand runs a slash command through the command handler.
func (r *Router) dispatchCommand(_ context.Context, msg InboundMessage) error {
	result, err := r.cmdHandler.HandleCommand(msg.Text)
	if err == nil {
		_ = msg.OnEvent(ipc.ChatEvent{Type: "system", Content: result})
	} else {
		_ = msg.OnEvent(ipc.ChatEvent{
			Type:    "system",
			Content: err.Error() + "\nType /help for available commands.",
		})
	}
	return msg.OnDone(TurnResult{})
}

// StartNotificationPump starts a goroutine that drains notifications for
// an instance and fans out the results to all bound channels. The pump
// runs for the lifetime of the context — typically the instance's lifetime.
// Call this once per instance (idempotent: duplicate calls for the same
// instance are no-ops).
func (r *Router) StartNotificationPump(ctx context.Context, instanceID string) {
	r.pumpMu.Lock()
	if _, running := r.pumps[instanceID]; running {
		r.pumpMu.Unlock()
		return
	}
	pumpCtx, cancel := context.WithCancel(ctx) //nolint:gosec // cancel stored in r.pumps, called by StopNotificationPump
	r.pumps[instanceID] = cancel
	r.pumpMu.Unlock()

	go r.runNotificationPump(pumpCtx, instanceID)
}

// StopNotificationPump cancels the pump for an instance.
func (r *Router) StopNotificationPump(instanceID string) {
	r.pumpMu.Lock()
	if cancel, ok := r.pumps[instanceID]; ok {
		cancel()
		delete(r.pumps, instanceID)
	}
	r.pumpMu.Unlock()
}

// runNotificationPump is the per-instance goroutine that watches the
// notification queue and delivers meta-turn results to bound channels.
func (r *Router) runNotificationPump(ctx context.Context, instanceID string) {
	defer func() {
		r.pumpMu.Lock()
		delete(r.pumps, instanceID)
		r.pumpMu.Unlock()
	}()

	q := r.manager.InstanceNotifications(instanceID)
	if q == nil {
		return
	}

	for {
		select {
		case <-q.Ready():
			r.drainAndDeliver(ctx, instanceID)
		case <-ctx.Done():
			return
		}
	}
}

// drainAndDeliver processes all pending notifications for an instance,
// triggering a meta inference turn for each and fanning out the result
// to all bound channels.
func (r *Router) drainAndDeliver(ctx context.Context, instanceID string) {
	q := r.manager.InstanceNotifications(instanceID)
	if q == nil {
		return
	}

	activeSession := r.manager.ActiveSessionID(instanceID)
	notifications := q.Drain()

	for _, n := range notifications {
		if n.SessionID != "" && n.SessionID != activeSession {
			r.logger.Info("discarding stale notification",
				"instance_id", instanceID,
				"source", n.Source,
				"notification_session", n.SessionID,
				"active_session", activeSession,
			)
			continue
		}

		if stop := r.processNotification(ctx, instanceID, n); stop {
			return
		}
	}
}

// processNotification runs a single meta inference turn for a notification
// and fans out the result. Returns true if draining should stop (terminal error).
func (r *Router) processNotification(ctx context.Context, instanceID string, n inference.Notification) bool {
	bindings := r.bindingsForInstance(instanceID)
	if len(bindings) == 0 {
		r.logger.Debug("no bindings for instance, skipping notification",
			"instance_id", instanceID,
			"source", n.Source,
		)
		return false
	}

	r.logger.Info("processing notification",
		"instance_id", instanceID,
		"source", n.Source,
		"content_length", len(n.Content),
		"binding_count", len(bindings),
	)

	var events []ipc.ChatEvent
	var eventsMu sync.Mutex
	onEvent := func(evt ipc.ChatEvent) error {
		eventsMu.Lock()
		events = append(events, evt)
		eventsMu.Unlock()
		return nil
	}

	response, turnErr := r.manager.SendMetaMessage(ctx, instanceID, n.Content, onEvent)
	if turnErr != nil {
		r.logger.Warn("notification turn failed",
			"instance_id", instanceID,
			"error", turnErr,
		)
	}

	result := TurnResult{
		Response: response,
		Usage:    r.usage.BuildUsageInfo(ctx, instanceID),
	}
	r.fanOut(ctx, bindings, events, result)

	return turnErr != nil && (errors.Is(turnErr, agent.ErrInstanceNotFound) ||
		errors.Is(turnErr, agent.ErrInstanceStopped))
}

// fanOut delivers events to all bound channels concurrently. Slow or
// broken channels don't block others.
func (r *Router) fanOut(ctx context.Context, bindings []*Binding, events []ipc.ChatEvent, result TurnResult) {
	var wg sync.WaitGroup
	for _, b := range bindings {
		wg.Add(1)
		go func(ch Channel, key string) {
			defer wg.Done()
			dctx, cancel := context.WithTimeout(ctx, deliverTimeout)
			defer cancel()
			if err := ch.Deliver(dctx, key, events, result); errors.Is(err, ErrChannelClosed) {
				r.Unbind(key)
			} else if err != nil {
				r.logger.Warn("notification delivery failed",
					"channel", ch.Name(),
					"conversation_key", key,
					"error", err,
				)
			}
		}(b.Channel, b.ConversationKey)
	}
	wg.Wait()
}

// Stop cancels all notification pumps and stops all registered channels.
func (r *Router) Stop() {
	// Cancel all pumps.
	r.pumpMu.Lock()
	for id, cancel := range r.pumps {
		cancel()
		delete(r.pumps, id)
	}
	r.pumpMu.Unlock()

	// Stop all channels.
	r.mu.RLock()
	channels := make([]Channel, 0, len(r.channels))
	for _, ch := range r.channels {
		channels = append(channels, ch)
	}
	r.mu.RUnlock()

	for _, ch := range channels {
		if err := ch.Stop(); err != nil {
			r.logger.Warn("channel stop failed", "channel", ch.Name(), "error", err)
		}
	}
}
